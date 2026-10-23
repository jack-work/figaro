package angelus_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/sdk"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type mockProviderForIntegration struct{}

func (m *mockProviderForIntegration) Name() string          { return "mock" }
func (m *mockProviderForIntegration) Fingerprint() string   { return "mock/v0" }
func (m *mockProviderForIntegration) SetModel(model string) {}
func (m *mockProviderForIntegration) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProviderForIntegration) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.TextContent("42"))
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("42")},
		StopReason: message.StopEnd,
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestIntegration_CreateAndPrompt(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, berr := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, berr)
	t.Cleanup(func() { _ = backend.Close() })
	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})

	factory := func(providerName string, knobs provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}
	loaded, err := config.Load(dir) // empty config dir, uses defaults
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: factory,
		Ctx:             ctx,
	}).Map

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	waitForAngelus(t, a.SocketPath)

	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	createResp, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	assert.NotEmpty(t, createResp.FigaroID)

	err = acli.Bind(ctx, 99999, createResp.FigaroID, 0)
	require.NoError(t, err)

	resolveResp, err := acli.Resolve(ctx, 99999)
	require.NoError(t, err)
	assert.True(t, resolveResp.Found)
	assert.Equal(t, createResp.FigaroID, resolveResp.FigaroID)

	listResp, err := acli.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listResp.Figaros, 1)
	assert.Equal(t, createResp.FigaroID, listResp.Figaros[0].ID)

	figaroEP := ariaAddr(a, createResp.FigaroID)

	doneCh := make(chan struct{}, 1)
	fcli, err := sdk.DialAria(figaroEP, func(method string, params json.RawMessage) {
		if method == "turn.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()

	_, _, err = fcli.Qua(ctx, "what is the answer?", nil)
	require.NoError(t, err)

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for turn.done")
	}

	cresp, err := fcli.Context(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(cresp.Messages), 2) // user + assistant

	_, err = acli.Kill(ctx, rpc.KillRequest{FigaroID: createResp.FigaroID, Now: true})
	require.NoError(t, err)

	listResp, err = acli.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listResp.Figaros)
}

func TestIntegration_Attach(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})

	factory := func(providerName string, knobs provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}
	loaded, err := config.Load(dir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: factory,
		Ctx:             ctx,
	}).Map

	go a.Run(ctx)
	defer a.Shutdown(0)

	waitForAngelus(t, a.SocketPath)

	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	_, err = acli.Attach(ctx, "no-such-aria")
	require.Error(t, err)

	_, err = acli.Attach(ctx, "bad/id")
	require.Error(t, err)

	resp, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.FigaroID)

	att, err := acli.Attach(ctx, resp.FigaroID)
	require.NoError(t, err)
	assert.Equal(t, resp.FigaroID, att.FigaroID)
}

func TestIntegration_Fork(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)
	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})
	factory := func(string, provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a, Config: loaded, ProviderFactory: factory, Ctx: ctx,
	}).Map
	go a.Run(ctx)
	defer a.Shutdown(0)

	waitForAngelus(t, a.SocketPath)
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	figEP := ariaAddr(a, created.FigaroID)
	doneCh := make(chan struct{}, 1)
	fcli, err := sdk.DialAria(figEP, func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	_, _, qerr := fcli.Qua(ctx, "first prompt", nil)
	require.NoError(t, qerr)
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout on turn")
	}
	fcli.Close()

	// The continuation keeps the trunk id; only the alternative is new.
	fr, err := acli.Fork(ctx, created.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, fr.Continuation, fr.Alternative)
	require.Equal(t, created.FigaroID, fr.Continuation)
	require.NotEqual(t, created.FigaroID, fr.Alternative)

	// Each child's form names its own id.
	formAriaID := func(id string) string {
		snap, serr := backend.FormState(id)
		require.NoError(t, serr)
		rawID, _ := snap.Get("aria_id")
		var got string
		require.NoError(t, json.Unmarshal(rawID, &got))
		return got
	}
	assert.Equal(t, fr.Continuation, formAriaID(fr.Continuation))
	assert.Equal(t, fr.Alternative, formAriaID(fr.Alternative))

	// Both children see the pre-fork prompt (shared prefix).
	countUser := func(id, want string) int {
		resp, rerr := acli.IR(ctx, id, 0, 0)
		require.NoError(t, rerr)
		n := 0
		for _, e := range resp.Entries {
			var m message.Message
			if json.Unmarshal(e.Payload, &m) != nil {
				continue
			}
			for _, c := range m.Content {
				if c.Type == message.ContentProse && c.Text == want {
					n++
				}
			}
		}
		return n
	}
	assert.Equal(t, 1, countUser(fr.Continuation, "first prompt"), "continuation shares prefix")
	assert.Equal(t, 1, countUser(fr.Alternative, "first prompt"), "alternative shares prefix")

	lst, err := acli.List(ctx)
	require.NoError(t, err)
	byID := map[string]rpc.FigaroInfoResponse{}
	for _, f := range lst.Figaros {
		byID[f.ID] = f
	}
	cont, alt := byID[fr.Continuation], byID[fr.Alternative]
	assert.Equal(t, created.FigaroID, cont.Trunk, "continuation keeps the trunk")
	assert.NotEqual(t, cont.Trunk, alt.Trunk, "alternative founds a new trunk")
	assert.Equal(t, alt.ID, alt.Trunk, "alternative trunk is itself")

	// Re-forking the same id adds another alternative; forking does not move you.
	fr2, err := acli.Fork(ctx, created.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err, "the trunk stays live and re-forkable")
	require.NotEqual(t, fr2.Continuation, fr2.Alternative)
	require.Equal(t, created.FigaroID, fr2.Continuation)
}
