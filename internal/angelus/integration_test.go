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
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// mockProviderForIntegration echoes "42" through the new bus shape.
type mockProviderForIntegration struct{}

func (m *mockProviderForIntegration) Name() string                                          { return "mock" }
func (m *mockProviderForIntegration) Fingerprint() string                                   { return "mock/v0" }
func (m *mockProviderForIntegration) SetModel(model string)                                 {}
func (m *mockProviderForIntegration) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (m *mockProviderForIntegration) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.TextContent("42"))
	msg := message.Message{
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent("42")},
		StopReason: message.StopEnd,
	}
	entry, err := in.FigStream.Append(store.Entry[message.Message]{Payload: msg}, true)
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
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
	factory := func(providerName, model string) (provider.Provider, error) {
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
	err = acli.Bind(ctx, 99999, createResp.FigaroID)
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

	// Connect with notification handler to wait for stream.done.
	// Notifications are delivered in wire order — no envelopes, no reordering.
	doneCh := make(chan struct{}, 1)
	fcli, err := figaro.DialClient(figaroEP, func(method string, params json.RawMessage) {
		if method == "stream.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()

	err = fcli.Qua(ctx, "what is the answer?", nil)
	require.NoError(t, err)

	// Wait for done notification.
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream.done")
	}

	// Verify via context that messages were processed.
	cresp, err := fcli.Context(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(cresp.Messages), 2) // user + assistant

	// --- Angelus client: kill ---
	err = acli.Kill(ctx, createResp.FigaroID)
	require.NoError(t, err)

	listResp, err = acli.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listResp.Figaros)
}

// TestIntegration_CreateWithID exercises the caller-supplied id path:
// success on a clean id, conflict against a live figaro, conflict
// against a dormant aria on disk, and validation failure.
func TestIntegration_CreateWithID(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))

	backend, err := store.NewFileBackend(dir + "/arias")
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: dir, Backend: backend})

	factory := func(providerName, model string) (provider.Provider, error) {
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

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

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

	// Happy path: caller-supplied id.
	resp, err := acli.CreateWithID(ctx, "my-named-aria", "mock", nil)
	require.NoError(t, err)
	assert.Equal(t, "my-named-aria", resp.FigaroID)

	// Conflict: same id while still live.
	_, err = acli.CreateWithID(ctx, "my-named-aria", "mock", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already live")

	// Persist the aria to disk so we can test the dormant-on-disk path.
	// Drive a turn through the figaro so SetMeta fires.
	figaroEP := transport.Endpoint{
		Scheme:  resp.Endpoint.Scheme,
		Address: resp.Endpoint.Address,
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(figaroEP.Address); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	doneCh := make(chan struct{}, 1)
	fcli, err := figaro.DialClient(figaroEP, func(method string, params json.RawMessage) {
		if method == "stream.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	require.NoError(t, fcli.Qua(ctx, "hello", nil))
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream.done")
	}
	fcli.Close()

	// Kill the live figaro; the aria stays on disk.
	require.NoError(t, acli.Kill(ctx, "my-named-aria"))

	// Recreate the backend's view by writing a meta file directly —
	// Kill removes the dir, so we need a fresh dormant aria to test
	// the on-disk conflict path. Use a different id.
	require.NoError(t, os.MkdirAll(dir+"/arias/persisted-aria", 0700))
	require.NoError(t, os.WriteFile(
		dir+"/arias/persisted-aria/meta.json",
		[]byte(`{"id":"persisted-aria","created_at":"2024-01-01T00:00:00Z"}`),
		0600,
	))

	_, err = acli.CreateWithID(ctx, "persisted-aria", "mock", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists on disk")

	// Validation: bad characters.
	_, err = acli.CreateWithID(ctx, "bad/id", "mock", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid char")

	// Validation: empty falls back to server-generated, no error.
	resp2, err := acli.CreateWithID(ctx, "", "mock", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp2.FigaroID)
	assert.NotEqual(t, "", resp2.FigaroID)
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

	backend, err := store.NewFileBackend(dir + "/arias")
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: dir, Backend: backend})

	factory := func(providerName, model string) (provider.Provider, error) {
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

	// Create a live aria, then Attach should be a no-op success.
	resp, err := acli.CreateWithID(ctx, "live-one", "mock", nil)
	require.NoError(t, err)
	assert.Equal(t, "live-one", resp.FigaroID)

	att, err := acli.Attach(ctx, "live-one")
	require.NoError(t, err)
	assert.Equal(t, "live-one", att.FigaroID)
	assert.Equal(t, resp.Endpoint.Address, att.Endpoint.Address)
}
