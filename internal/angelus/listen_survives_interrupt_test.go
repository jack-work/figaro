package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// switchProvider returns different behavior on each Send call: the
// first call streams a delta then blocks until ctx.Done() (mid-turn
// interrupt target); subsequent calls reply with a fixed string and
// seal cleanly. Meant for reproducing the "output never arrives"
// scenario over a live angelus + figaro socket.
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
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent(p.fastText)},
		StopReason: message.StopEnd,
	}
	e, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = e.LT
	bus.PushFigaro(msg)
	return nil
}

// TestListenSurvivesConcurrentInterruptedSend drives an end-to-end
// scenario: a `figaro listen`-style subscriber holds a live connection
// while a first `figaro send`-style client submits + interrupts mid-turn,
// then a second send submits a fresh prompt. The listener must receive
// the second turn's frames.
//
// This is the reported bug: on Linux, output after `figaro send` sometimes
// never arrives over a concurrent `figaro listen`. Strong lead: the
// mid-turn interrupt breaks read/subscribe coherence on the aria.
func TestListenSurvivesConcurrentInterruptedSend(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/switch.toml", []byte(`
[system]
provider = "switch"
model = "m"
`), 0600))

	backend, err := store.NewXwalBackend(dir + "/arias")
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

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	defer acli.Close()

	create, err := acli.Create(ctx, "switch", nil)
	require.NoError(t, err)
	figEP := transport.Endpoint{Scheme: create.Endpoint.Scheme, Address: create.Endpoint.Address}
	waitForFigaro(t, figEP)

	// -- Listener: subscribes and stays connected across both sends. --
	listenerFrames := make(chan rpc.Notification, 256)
	listener, err := figaro.DialClient(figEP, func(method string, params json.RawMessage) {
		select {
		case listenerFrames <- rpc.Notification{Method: method, Params: append(json.RawMessage(nil), params...)}:
		default:
		}
	})
	require.NoError(t, err)
	defer listener.Close()

	// -- Send 1: submit + wait mid-turn + interrupt + disconnect --
	send1Frames := make(chan rpc.Notification, 128)
	sender1, err := figaro.DialClient(figEP, func(method string, params json.RawMessage) {
		select {
		case send1Frames <- rpc.Notification{Method: method, Params: append(json.RawMessage(nil), params...)}:
		default:
		}
	})
	require.NoError(t, err)

	_, _, err = sender1.Qua(ctx, "please stream forever", nil)
	require.NoError(t, err)

	// Wait for the provider to hit its blocking wait.
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

	// Interrupt, wait for turn.done, then disconnect.
	require.NoError(t, sender1.Interrupt(ctx))
	waitForMethod(t, send1Frames, rpc.MethodTurnDone, 3*time.Second)
	sender1.Close()

	// Give the daemon a moment to process the disconnect cleanup.
	time.Sleep(50 * time.Millisecond)

	// -- Send 2: submit a fresh prompt on a NEW connection. --
	send2Done := make(chan struct{}, 1)
	sender2, err := figaro.DialClient(figEP, func(method string, params json.RawMessage) {
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

	// -- The listener must have received both turn.done notifications AND
	//    aria frames for at least one committed message after the second turn.
	// Drain listenerFrames for up to 500ms and count what we saw.
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
					Committed []json.RawMessage `json:"committed,omitempty"`
					Live      json.RawMessage   `json:"live,omitempty"`
				}
				if raw, ok := n.Params.(json.RawMessage); ok {
					if json.Unmarshal(raw, &r) == nil {
						committedCount += len(r.Committed)
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
