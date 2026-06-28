package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// mockProviderForIntegration echoes "42" through the new bus shape.
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
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent("42")},
		StopReason: message.StopEnd,
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestIntegration_CreateAndPrompt(t *testing.T) {
	dir := t.TempDir()

	// Mock loadout — the create path resolves it via outfit.
	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	// Create and start angelus.
	a := angelus.New(angelus.Config{RuntimeDir: dir})

	// Wire the provider factory.
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

	// Wait for socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// --- Angelus client: create a figaro ---
	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	createResp, err := acli.Create(ctx, "mock", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, createResp.FigaroID)
	assert.Equal(t, "unix", createResp.Endpoint.Scheme)

	// --- Angelus client: bind a pid ---
	err = acli.Bind(ctx, 99999, createResp.FigaroID, 0)
	require.NoError(t, err)

	// --- Angelus client: resolve ---
	resolveResp, err := acli.Resolve(ctx, 99999)
	require.NoError(t, err)
	assert.True(t, resolveResp.Found)
	assert.Equal(t, createResp.FigaroID, resolveResp.FigaroID)

	// --- Angelus client: list ---
	listResp, err := acli.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listResp.Figaros, 1)
	assert.Equal(t, createResp.FigaroID, listResp.Figaros[0].ID)

	// --- Figaro client: connect and prompt ---
	// Wait for figaro socket to appear.
	figaroEP := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(figaroEP.Address); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect with notification handler to wait for turn.done.
	// Notifications are delivered in wire order — no envelopes, no reordering.
	doneCh := make(chan struct{}, 1)
	fcli, err := figaro.DialClient(figaroEP, func(method string, params json.RawMessage) {
		if method == "turn.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()

	_, err = fcli.Qua(ctx, "what is the answer?", nil)
	require.NoError(t, err)

	// Wait for done notification.
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for turn.done")
	}

	// Verify via context that messages were processed.
	cresp, err := fcli.Context(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(cresp.Messages), 2) // user + assistant

	// --- Angelus client: kill ---
	err = acli.Kill(ctx, createResp.FigaroID, false)
	require.NoError(t, err)

	listResp, err = acli.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listResp.Figaros)
}

// TestIntegration_Attach exercises figaro.attach for live, dormant,
// and unknown ids.
func TestIntegration_Attach(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewXwalBackend(dir + "/arias")
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: dir, Backend: backend})

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

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	// Unknown id → error.
	_, err = acli.Attach(ctx, "no-such-aria")
	require.Error(t, err)

	// Bad id → error.
	_, err = acli.Attach(ctx, "bad/id")
	require.Error(t, err)

	// Create a live aria (system-minted id), then Attach is a no-op success.
	resp, err := acli.Create(ctx, "mock", nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.FigaroID)

	att, err := acli.Attach(ctx, resp.FigaroID)
	require.NoError(t, err)
	assert.Equal(t, resp.FigaroID, att.FigaroID)
	assert.Equal(t, resp.Endpoint.Address, att.Endpoint.Address)
}

// TestIntegration_Fork drives a turn, forks the conversation, and
// verifies both children share the pre-fork prefix while diverging
// independently — the whole daemon fork path end to end (mock provider).
func TestIntegration_Fork(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewXwalBackend(dir + "/arias")
	require.NoError(t, err)
	a := angelus.New(angelus.Config{RuntimeDir: dir, Backend: backend})
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

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	// Create + drive one turn.
	created, err := acli.Create(ctx, "mock", nil)
	require.NoError(t, err)
	figEP := transport.Endpoint{Scheme: created.Endpoint.Scheme, Address: created.Endpoint.Address}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(figEP.Address); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	doneCh := make(chan struct{}, 1)
	fcli, err := figaro.DialClient(figEP, func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	_, qerr := fcli.Qua(ctx, "first prompt", nil)
	require.NoError(t, qerr)
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout on turn")
	}
	fcli.Close()

	// Fork (trunk model): the aria id is STABLE — the continuation keeps it
	// (cont == id, bind-to-trunk), only the alternative is new.
	fr, err := acli.Fork(ctx, created.FigaroID, 0)
	require.NoError(t, err)
	require.NotEqual(t, fr.Continuation, fr.Alternative)
	require.Equal(t, created.FigaroID, fr.Continuation)
	require.NotEqual(t, created.FigaroID, fr.Alternative)

	// Both children see the pre-fork prompt (shared prefix).
	countUser := func(id, want string) int {
		resp, rerr := acli.AriaRead(ctx, id, 0, 0)
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

	// Trunk model: the continuation keeps the original trunk id (cont ==
	// created.FigaroID); the alternative founds its own trunk.
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

	// The trunk is stable and stays live — re-forking the same id again
	// just adds another alternative (bind-to-trunk: forking doesn't move you).
	fr2, err := acli.Fork(ctx, created.FigaroID, 0)
	require.NoError(t, err, "the trunk stays live and re-forkable")
	require.NotEqual(t, fr2.Continuation, fr2.Alternative)
	require.Equal(t, created.FigaroID, fr2.Continuation)
}
