package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
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

// namedMockProvider answers with its own name, so the transcript records
// which provider actually served a round.
type namedMockProvider struct{ name string }

func (m *namedMockProvider) Name() string        { return m.name }
func (m *namedMockProvider) Fingerprint() string { return m.name + "/v0" }
func (m *namedMockProvider) SetModel(string)     {}
func (m *namedMockProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *namedMockProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("served by " + m.name)},
		StopReason: message.StopEnd,
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

// TestIntegration_ProviderSwitchMidConversation drives the whole daemon path
// of the bug: a live aria whose provider is wedged must be movable to another
// provider with `figaro set system.provider …`, with no restart and no fork.
func TestIntegration_ProviderSwitchMidConversation(t *testing.T) {
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

	var mu sync.Mutex
	var asked []string
	factory := func(name string, _ provider.Knobs) (provider.Provider, error) {
		mu.Lock()
		asked = append(asked, name)
		mu.Unlock()
		return &namedMockProvider{name: name}, nil
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

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	resp, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	ep := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	waitForFigaro(t, ep)

	doneCh := make(chan struct{}, 4)
	fcli, err := figaro.DialClient(ep, func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()

	waitDone := func() {
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for turn.done")
		}
	}

	_, _, err = fcli.Qua(ctx, "first", nil)
	require.NoError(t, err)
	waitDone()

	// The switch: form state, no restart.
	_, err = fcli.Set(ctx, rpc.FormPatch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"other"`),
	}}, 0)
	require.NoError(t, err)

	_, _, err = fcli.Qua(ctx, "second", nil)
	require.NoError(t, err)
	waitDone()

	cresp, err := fcli.Context(ctx)
	require.NoError(t, err)
	var served []string
	for _, raw := range cresp.Messages {
		b, err := json.Marshal(raw)
		require.NoError(t, err)
		var m message.Message
		require.NoError(t, json.Unmarshal(b, &m))
		if m.Role != message.RoleOutput {
			continue
		}
		for _, c := range m.Content {
			served = append(served, c.Text)
		}
	}
	require.Len(t, served, 2)
	assert.Equal(t, "served by mock", served[0])
	assert.Equal(t, "served by other", served[1], "the round after the switch must run on the new provider")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"mock", "other"}, asked, "the agent must ask the factory for the new provider")

	// The daemon's view of the aria agrees.
	list, err := acli.List(ctx)
	require.NoError(t, err)
	require.Len(t, list.Figaros, 1)
	assert.Equal(t, "other", list.Figaros[0].Provider)
}
