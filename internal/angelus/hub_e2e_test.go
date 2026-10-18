package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

func daemonFixture(t testing.TB) (*angelus.Angelus, *sdk.Angelus, context.Context) {
	a, acli, ctx, _ := daemonFixtureDir(t)
	return a, acli, ctx
}

func daemonFixtureDir(t testing.TB) (*angelus.Angelus, *sdk.Angelus, context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))
	// A named default makes the lifecycle observable: the empty form's hash never moves.
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})
	loaded, err := config.Load(dir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a,
		Config:  loaded,
		ProviderFactory: func(name string, _ provider.Knobs) (provider.Provider, error) {
			// Real factories refuse an unknown provider; the naked-figaro test needs the refusal.
			if name != "mock" {
				return nil, fmt.Errorf("unknown provider %q", name)
			}
			return &mockProviderForIntegration{}, nil
		},
		Ctx: ctx,
		// Without FormTemplates restoreOne fails "form unavailable".
		FormTemplates: template.New("t"),
	}).Map

	go a.Run(ctx)
	t.Cleanup(func() { a.Shutdown(0) })
	waitForAngelus(t, a.SocketPath)

	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	t.Cleanup(func() { acli.Close() })
	return a, acli, ctx, dir
}

// ariaConn is one connection on the door addressing one aria, the way
// sdk.Aria does, without sdk's conveniences.
type ariaConn struct {
	*jkrpc.Client
	id string
}

func (c ariaConn) Call(ctx context.Context, method string, params, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := rpc.WithAria(b, c.id)
	if err != nil {
		return err
	}
	return c.Client.Call(ctx, method, raw, out)
}

func dialAria(t testing.TB, a *angelus.Angelus, id string, onNotify func(string, json.RawMessage)) ariaConn {
	t.Helper()
	conn, err := net.Dial("unix", a.SocketPath)
	require.NoError(t, err)
	c := jkrpc.NewClient(jkrpc.NewConn(conn), onNotify)
	t.Cleanup(func() { c.Close() })
	return ariaConn{Client: c, id: id}
}

func ariaAddr(a *angelus.Angelus, id string) sdk.Addr {
	return sdk.Addr{Door: transport.UnixEndpoint(a.SocketPath), ID: id}
}

func TestEndpointOutlivesAgent(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	frames := make(chan string, 16)
	client := dialAria(t, a, created.FigaroID, func(m string, _ json.RawMessage) {
		frames <- m
	})

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var before rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &before))

	require.NoError(t, a.Registry.Kill(created.FigaroID))
	require.Nil(t, a.Registry.Get(created.FigaroID), "agent still registered")

	// The same connection still answers, from the store, with no agent rebuilt.
	var after rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &after),
		"attached client lost its connection when the agent died")
	require.Nil(t, a.Registry.Get(created.FigaroID), "a read woke the aria")

	var cx rpc.ContextResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodContext, struct{}{}, &cx))
	require.NotEmpty(t, cx.Messages, "store served an empty context")
	require.Nil(t, a.Registry.Get(created.FigaroID), "context woke the aria")

	var page map[string]any
	require.NoError(t, client.Call(callCtx, rpc.MethodRead, rpc.ReadRequest{}, &page))
	require.Nil(t, a.Registry.Get(created.FigaroID), "read woke the aria")
}

func TestPromptWakesReclaimedAria(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	require.NoError(t, a.Registry.Kill(created.FigaroID))
	require.Nil(t, a.Registry.Get(created.FigaroID))

	client := dialAria(t, a, created.FigaroID, func(string, json.RawMessage) {})

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out rpc.QuaResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodQua,
		rpc.QuaRequest{Text: "hello after reclamation"}, &out))
	require.True(t, out.OK)

	require.Eventually(t, func() bool {
		return a.Registry.Get(created.FigaroID) != nil
	}, 5*time.Second, 20*time.Millisecond, "prompt did not wake the aria")
}

// A kill closes the endpoint: a follower is told, and the aria answers no more.
func TestKillRemovesEndpoint(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	closed := make(chan string, 4)
	client := dialAria(t, a, created.FigaroID, func(m string, p json.RawMessage) {
		if m == rpc.MethodAriaClosed {
			closed <- rpc.AriaOf(p)
		}
	})
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var followed rpc.FollowResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodFollow, nil, &followed))

	require.NoError(t, acli.Kill(ctx, created.FigaroID, false))
	select {
	case id := <-closed:
		require.Equal(t, created.FigaroID, id)
	case <-time.After(5 * time.Second):
		t.Fatal("the follower was not told the aria closed")
	}
	var after rpc.FormResponse
	require.ErrorContains(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &after), "not found")
}

// Reads answer and never subscribe: a connection that only reads collects no
// pushes, however busy the aria is.
func TestReadOnlyConnectionCollectsNoPushes(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	pushes := make(chan string, 64)
	reader := dialAria(t, a, created.FigaroID, func(m string, _ json.RawMessage) { pushes <- m })
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var form rpc.FormResponse
	require.NoError(t, reader.Call(callCtx, rpc.MethodForm, struct{}{}, &form))
	var cx rpc.ContextResponse
	require.NoError(t, reader.Call(callCtx, rpc.MethodContext, struct{}{}, &cx))
	var page map[string]any
	require.NoError(t, reader.Call(callCtx, rpc.MethodRead, rpc.ReadRequest{}, &page))
	var ir rpc.IRResponse
	require.NoError(t, reader.Call(callCtx, rpc.MethodIR, rpc.IRRequest{FigaroID: created.FigaroID}, &ir))
	var list rpc.ListResponse
	require.NoError(t, reader.Client.Call(callCtx, rpc.MethodList, rpc.ListRequest{}, &list))

	done := make(chan struct{}, 1)
	sender := dialAria(t, a, created.FigaroID, func(m string, _ json.RawMessage) {
		if m == rpc.MethodTurnDone {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	var out rpc.QuaResponse
	require.NoError(t, sender.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "busy"}, &out))
	var set rpc.SetResponse
	require.NoError(t, sender.Call(callCtx, rpc.MethodSet, rpc.SetRequest{Patch: *rawPatch(map[string]string{"mantra": `"busy"`})}, &set))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sender, which prompted, did not hear its turn end")
	}
	select {
	case m := <-pushes:
		t.Fatalf("a read-only connection collected %s", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// No per-aria socket is ever created: the runtime dir holds the door alone.
func TestRuntimeDirHasNoAriaSockets(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	client := dialAria(t, a, created.FigaroID, nil)
	var out rpc.FormResponse
	require.NoError(t, client.Call(ctx, rpc.MethodForm, struct{}{}, &out))
	_, err = os.Stat(filepath.Join(a.RuntimeDir, "figaros"))
	require.True(t, os.IsNotExist(err), "a figaros/ directory exists")
}

// The door is one method map over any listener: the same calls reach the same
// aria over TCP as over the unix socket.
func TestDoorServesTCP(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	admitted := make(chan struct{}, 1)
	go a.Serve(ctx, ln, func(ctx context.Context, _ net.Conn) (context.Context, error) {
		admitted <- struct{}{}
		return ctx, nil
	})

	tcp := sdk.Addr{Door: transport.Endpoint{Scheme: "tcp", Address: ln.Addr().String()}, ID: created.FigaroID}
	fc, err := sdk.DialAria(tcp, nil)
	require.NoError(t, err)
	defer fc.Close()
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = fc.Set(callCtx, *rawPatch(map[string]string{"mantra": `"over tcp"`}), 0)
	require.NoError(t, err)
	<-admitted

	unix, err := sdk.DialAria(ariaAddr(a, created.FigaroID), nil)
	require.NoError(t, err)
	defer unix.Close()
	resp, err := unix.Form(callCtx)
	require.NoError(t, err)
	raw, ok := resp.Snapshot.Get("mantra")
	require.True(t, ok)
	require.Equal(t, `"over tcp"`, string(raw))
}

// An admitter that stalls (a TLS handshake that never finishes) holds only
// its own connection; the next caller is admitted and served.
func TestDoorAdmitsOffTheAcceptLoop(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	release := make(chan struct{})
	defer close(release)
	var first atomic.Bool
	go a.Serve(ctx, ln, func(ctx context.Context, _ net.Conn) (context.Context, error) {
		if first.CompareAndSwap(false, true) {
			<-release
		}
		return ctx, nil
	})

	stalled, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer stalled.Close()
	require.Eventually(t, first.Load, 5*time.Second, time.Millisecond)

	tcp := sdk.Addr{Door: transport.Endpoint{Scheme: "tcp", Address: ln.Addr().String()}, ID: created.FigaroID}
	fc, err := sdk.DialAria(tcp, nil)
	require.NoError(t, err)
	defer fc.Close()
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = fc.Form(callCtx)
	require.NoError(t, err)
}

// A bounded listing walks every aria exactly once, most recent first, with
// each page enriched as a full listing would be.
func TestBoundedListingThroughTheDaemon(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	want := map[string]bool{}
	for range 5 {
		created, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		want[created.FigaroID] = true
	}
	full, err := acli.List(ctx)
	require.NoError(t, err)
	require.Empty(t, full.NextBefore)

	seen := map[string]int{}
	var last int64 = 1 << 62
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10, "paging never ended")
		resp, err := acli.ListWith(ctx, rpc.ListRequest{Limit: 2, Before: cursor})
		require.NoError(t, err)
		require.LessOrEqual(t, len(resp.Figaros), 2)
		for _, r := range resp.Figaros {
			seen[r.ID]++
			require.LessOrEqual(t, r.LastActive, last, "rows out of recency order")
			last = r.LastActive
			if want[r.ID] {
				require.Equal(t, "mock", r.OutfitName, "a paged row was not enriched")
			}
		}
		if resp.NextBefore == "" {
			break
		}
		cursor = resp.NextBefore
	}
	for id := range want {
		require.Equal(t, 1, seen[id], "aria %s", id)
	}
	require.Equal(t, len(full.Figaros), len(seen))

	_, err = acli.ListWith(ctx, rpc.ListRequest{Limit: 2, Before: "garbage"})
	require.Error(t, err, "a malformed cursor restarted from the top")

	for id := range want {
		f, err := acli.Form(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, f)
		break
	}
}
