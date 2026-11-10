package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type edgeStub struct {
	inner    http.RoundTripper
	upgrades atomic.Int32
}

func (e *edgeStub) RoundTrip(r *http.Request) (*http.Response, error) {
	e.upgrades.Add(1)
	r = r.Clone(r.Context())
	r.Header.Set("Remote-User", "admin")
	return e.inner.RoundTrip(r)
}

// edgeServed is a real angelus over a real store with a web door that trusts
// the edge's headers and ends follows at maxAge, and the wss endpoint a CLI
// behind the edge would dial. The edge is a stub that names the owner.
func edgeServed(t *testing.T, maxAge time.Duration) (*sdk.Angelus, transport.Endpoint, *edgeStub, string) {
	t.Helper()
	backend, id := store.NewTestAria(t, "edge", message.Patch{})
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "f")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := t.TempDir()
	policy := filepath.Join(cfg, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte(`
[roles.owner]
grants = ["*:*"]
[[bind]]
role = "owner"
principals = ["user:`+strconv.Itoa(os.Getuid())+`", "user:admin@auth.test"]
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[authz]\npolicy = \""+policy+"\"\n[authz.upstream]\nissuer = \"https://auth.test\"\n"), 0o600))
	loaded, err := config.Load(cfg)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{Backend: backend, RuntimeDir: dir})
	pc, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	require.NoError(t, err)
	a.Peercred = pc
	a.Policies = authz.NewPolicies(pc.Self(), angelus.StoreFacts(backend))
	require.NoError(t, a.Policies.Reload(policy))
	ctx, cancel := context.WithCancel(context.Background())
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a, Config: loaded, Ctx: ctx,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) {
			return nil, nil
		},
	}).Map
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	var acli *sdk.Angelus
	require.Eventually(t, func() bool {
		acli, err = sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "angelus did not come up")
	t.Cleanup(func() { acli.Close() })

	web := filepath.Join(dir, "web.sock")
	u, err := authz.NewUpstream(loaded.Config.Authz.Upstream, web, a.SocketPath, nil)
	require.NoError(t, err)
	ln, err := angelus.ListenUnix(web, 0o660)
	require.NoError(t, err)
	go a.ServeWeb(ctx, ln, angelus.Web{Admit: u.AdmitRequest, FollowMaxAge: maxAge})

	stub := &edgeStub{inner: &http.Transport{DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", web)
	}}}
	ep := transport.Endpoint{Scheme: "wss", Address: "wss://fig.test/", HTTPClient: &http.Client{Transport: stub}}
	return acli, ep, stub, id
}

// A pager following through the edge is cut at the follow age; it dials
// the edge again and follows the same aria with no keystroke, and the
// session does not report its subject dead.
func TestAPagerFollowsAgainWhenTheEdgeEndsItsFollow(t *testing.T) {
	const maxAge = 300 * time.Millisecond
	acli, ep, stub, id := edgeServed(t, maxAge)

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := &interactiveInput{
		mu: new(renderLock), set: &renderSettings{}, life: ctx, acli: acli,
		ownsSubject: true, subjectDead: make(chan struct{}, 1), intrinsics: newIntrinsicMirrors(),
	}
	in.lt = newLivelogTurn(&out, 80, 20, in.set, id, time.Now(), newSessionStatus(id, time.Now()), nil, dimRule)
	require.NoError(t, in.retarget(ctx, id, sdk.Addr{Door: ep, ID: id}))
	first := in.aria()
	require.EqualValues(t, 1, stub.upgrades.Load())

	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the edge door never ended the follow")
	}
	require.True(t, first.Lapsed(), "the door ended the connection without saying it lapsed")
	require.Eventually(t, func() bool { return in.aria() != first }, 5*time.Second, 10*time.Millisecond, "the pager did not follow again")
	require.EqualValues(t, 2, stub.upgrades.Load(), "the re-follow did not go back through the edge")
	require.Equal(t, id, in.currentID())
	again := in.aria()
	select {
	case <-again.Done():
		t.Fatal("the new follow is already dead")
	case <-in.subjectDead:
		t.Fatal("a lapse was reported as a dead subject")
	default:
	}

	cancel()
	select {
	case <-again.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the second follow never lapsed")
	}
	select {
	case <-in.subjectDead:
	case <-time.After(2 * time.Second):
		t.Fatal("once the session has ended, a lapse is a dead subject")
	}
}

// A mounted form is opened the way form listen opens one, so it follows again
// on a lapse too: its Done stays open, an edit made after two lapses lands
// through the new follow, and closing the mount releases it and ends Done.
func TestAMountedFormFollowsAgainWhenTheEdgeEndsItsFollow(t *testing.T) {
	const maxAge = 250 * time.Millisecond
	_, ep, stub, id := edgeServed(t, maxAge)

	var released atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	view, closeView, done, err := openFormViewOn(ctx, cancel, sdk.Addr{Door: ep, ID: id}, id, "", func() {}, func() { released.Add(1) })
	require.NoError(t, err)

	require.Eventually(t, func() bool { return stub.upgrades.Load() >= 3 }, 5*time.Second, 10*time.Millisecond, "the mount did not follow again after two lapses")
	select {
	case <-done:
		t.Fatal("a lapse ended the mount's link")
	default:
	}

	src, ok := view.src.(replicaSource)
	require.True(t, ok, "the mount does not edit through a replica")
	require.NoError(t, src.Edit(form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"after the lapses"`)}, nil)))
	require.Eventually(t, func() bool {
		v := src.Shown().Lookup("mantra")
		return src.Pending() == 0 && v != nil && *v == "after the lapses"
	}, 5*time.Second, 10*time.Millisecond, "an edit after the lapses did not land")

	closeView()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the mount left its link open")
	}
	require.EqualValues(t, 1, released.Load())
}
