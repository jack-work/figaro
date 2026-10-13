package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"net"
	"os"
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

func TestEndpointOutlivesAgent(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	sock := created.Endpoint.Address
	require.FileExists(t, sock, "endpoint not listening when Create returned")

	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	frames := make(chan string, 16)
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(m string, _ json.RawMessage) {
		frames <- m
	})
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var before rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &before))

	require.NoError(t, a.Registry.Kill(created.FigaroID))
	require.Nil(t, a.Registry.Get(created.FigaroID), "agent still registered")

	// The same connection still answers, from the store, with no agent rebuilt.
	require.FileExists(t, sock, "endpoint vanished with the agent")
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

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
	defer client.Close()

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

func TestKillRemovesEndpoint(t *testing.T) {
	_, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	sock := created.Endpoint.Address
	require.FileExists(t, sock)

	require.NoError(t, acli.Kill(ctx, created.FigaroID, false))
	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "socket survived a kill")
}
