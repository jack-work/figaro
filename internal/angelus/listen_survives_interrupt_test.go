package angelus_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/sdk"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// switchProvider streams a delta then blocks until ctx.Done() on the first
// Send; later Sends seal cleanly.
type switchProvider struct {
	mu       sync.Mutex
	calls    atomic.Int64
	started  chan struct{}
	fastText string
}

func (p *switchProvider) Name() string        { return "switch" }
func (p *switchProvider) Fingerprint() string { return "switch/v0" }
func (p *switchProvider) SetModel(string)     {}
func (p *switchProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *switchProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	n := p.calls.Add(1)
	if n == 1 {
		bus.PushDelta(message.TextContent("first, streaming..."))
		p.mu.Lock()
		started := p.started
		p.started = nil
		p.mu.Unlock()
		if started != nil {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	bus.PushDelta(message.TextContent(p.fastText))
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent(p.fastText)},
		StopReason: message.StopEnd,
	}
	bus.PushFigaro(msg)
	return nil
}

// A listener must receive the second turn's frames after a concurrent client
// submits and interrupts mid-turn.
func TestListenSurvivesConcurrentInterruptedSend(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/switch.toml", []byte(`
[system]
provider = "switch"
model = "m"
`), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})

	sp := &switchProvider{started: make(chan struct{}), fastText: "final reply"}
	factory := func(name string, knobs provider.Knobs) (provider.Provider, error) {
		return sp, nil
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

	create, err := acli.Create(ctx, dress(t, "switch"), nil)
	require.NoError(t, err)
	figEP := ariaAddr(a, create.FigaroID)

	listenerFrames := make(chan rpc.Notification, 256)
	listener, err := sdk.DialAria(figEP, func(method string, params json.RawMessage) {
		select {
		case listenerFrames <- rpc.Notification{Method: method, Params: append(json.RawMessage(nil), params...)}:
		default:
		}
	})
	require.NoError(t, err)
	defer listener.Close()

	send1Frames := make(chan rpc.Notification, 128)
	sender1, err := sdk.DialAria(figEP, func(method string, params json.RawMessage) {
		select {
		case send1Frames <- rpc.Notification{Method: method, Params: append(json.RawMessage(nil), params...)}:
		default:
		}
	})
	require.NoError(t, err)

	_, _, err = sender1.Qua(ctx, "please stream forever", nil)
	require.NoError(t, err)

	sp.mu.Lock()
	startedCh := sp.started
	sp.mu.Unlock()
	if startedCh == nil {
		startedCh = make(chan struct{})
		close(startedCh)
	}
	select {
	case <-startedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never entered blocking Send")
	}

	require.NoError(t, sender1.Interrupt(ctx))
	waitForMethod(t, send1Frames, rpc.MethodTurnDone, 3*time.Second)
	sender1.Close()

	time.Sleep(50 * time.Millisecond)

	send2Done := make(chan struct{}, 1)
	sender2, err := sdk.DialAria(figEP, func(method string, params json.RawMessage) {
		if method == rpc.MethodTurnDone {
			select {
			case send2Done <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer sender2.Close()

	_, _, err = sender2.Qua(ctx, "please answer quickly", nil)
	require.NoError(t, err)

	select {
	case <-send2Done:
	case <-time.After(5 * time.Second):
		t.Fatal("second turn never completed")
	}

	deadline := time.After(500 * time.Millisecond)
	var doneCount int
	var committedCount int
drain:
	for {
		select {
		case n := <-listenerFrames:
			switch n.Method {
			case rpc.MethodTurnDone:
				doneCount++
			case rpc.MethodAriaFrame:
				var r struct {
					Parts []json.RawMessage `json:"parts,omitempty"`
				}
				if raw, ok := n.Params.(json.RawMessage); ok {
					if json.Unmarshal(raw, &r) == nil {
						committedCount += len(r.Parts)
					}
				}
			}
		case <-deadline:
			break drain
		}
	}
	if doneCount < 2 {
		t.Fatalf("listener saw %d turn.done, want >=2 (one per send)", doneCount)
	}
	if committedCount == 0 {
		t.Fatalf("listener saw 0 committed frames after the two sends; the fanout stream went silent")
	}
}

func waitForMethod(t *testing.T, ch <-chan rpc.Notification, method string, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case n := <-ch:
			if n.Method == method {
				return
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %s", method)
		}
	}
}
