package figaro_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// slowStreamingProvider streams a delta, then blocks, so the on-disk tail is a bare user prompt.
type slowStreamingProvider struct {
	started chan struct{}
}

func (p *slowStreamingProvider) Name() string        { return "slow-stream" }
func (p *slowStreamingProvider) Fingerprint() string { return "slow-stream/v0" }
func (p *slowStreamingProvider) SetModel(string)     {}
func (p *slowStreamingProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *slowStreamingProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: "partial thought"})
	if p.started != nil {
		close(p.started)
		p.started = nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// After a mid-turn interrupt, a fresh agent on the same backend serves Read(0)
// and a live subscriber receives frames on the next prompt.
func TestReadSubscribeAfterInterruptedTurn(t *testing.T) {
	t.Parallel()
	testReadSubscribeAfterInterrupt(t, true)
}

// The same scenario on the same agent: only the turn was cancelled.
func TestReadSubscribeAfterInterrupt_SameAgent(t *testing.T) {
	t.Parallel()
	testReadSubscribeAfterInterrupt(t, false)
}

func testReadSubscribeAfterInterrupt(t *testing.T, restart bool) {
	dir := t.TempDir()

	backend1, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	outfit, err := backend1.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m"`),
		"system.provider": json.RawMessage(`"slow-stream"`),
	}, nil))
	require.NoError(t, err)
	conv, err := backend1.CreateConversation(outfit)
	require.NoError(t, err)

	started := make(chan struct{})
	a1 := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         conv,
		SocketPath: filepath.Join(t.TempDir(), "a1.sock"),
		Provider:   &slowStreamingProvider{started: started},
		Backend:    backend1,
	})

	sink1 := make(chan rpc.Notification, 64)
	unsub := a1.Subscribe(&reproNotifier{ch: sink1})

	a1.SubmitPrompt(rpc.QuaRequest{Text: "please stream forever"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never entered Send")
	}

	a1.Interrupt()

	waitFor(t, sink1, rpc.MethodTurnDone, 3*time.Second)
	unsub()

	// A subscriber attached after the interrupt sees the content on catch-up and live frames next turn.
	lateSink := make(chan rpc.Notification, 128)
	lateUnsub := a1.Subscribe(&reproNotifier{ch: lateSink})
	defer lateUnsub()

	lateInit := a1.Read(aria.Anchor{Turn: 0}, 1<<20)
	if len(lateInit.Parts) == 0 {
		t.Fatalf("late Read(0) after interrupt returned 0 committed units")
	}

	var backend2 store.Backend
	var a2 *figaro.Agent
	if restart {
		lateUnsub()
		a1.Kill()
		require.NoError(t, backend1.Close())
		var err error
		backend2, err = store.NewXwalBackend(dir, 0)
		require.NoError(t, err)
		t.Cleanup(func() { backend2.Close() })
		a2 = figaro.NewAgent(figaro.Config{
			Projector:  uiir.New(nil),
			ID:         conv,
			SocketPath: filepath.Join(t.TempDir(), "a2.sock"),
			Provider:   &staticReplyProvider{reply: "second reply after restore"},
			Backend:    backend2,
		})
		t.Cleanup(a2.Kill)
	} else {
		a2 = a1
		t.Cleanup(a2.Kill)
	}

	got := a2.Read(aria.Anchor{Turn: 0}, 1<<20)
	if len(got.Parts) == 0 {
		t.Fatalf("Read(0) after interrupt+restore returned 0 units; want >=1 (user prompt on disk)")
	}
	sawUser := false
	for _, c := range got.Parts {
		if c.Inquiry == "please stream forever" {
			sawUser = true
		}
	}
	require.True(t, sawUser, "Read(0) should include the on-disk user prompt")

	sink2 := make(chan rpc.Notification, 64)
	unsub2 := a2.Subscribe(&reproNotifier{ch: sink2})
	defer unsub2()

	if !restart {
		return // same-agent path: we've verified Read(0) still returns content
	}

	a2.SubmitPrompt(rpc.QuaRequest{Text: "hello again"})
	waitFor(t, sink2, rpc.MethodTurnDone, 5*time.Second)

	// Two turns, not three units: the question belongs to the turn that answered it.
	final := a2.Read(aria.Anchor{Turn: 0}, 1<<20)
	require.GreaterOrEqual(t, len(final.Parts), 2,
		"final Read(0) should contain both the original turn and the new one")
	prompts := 0
	for _, part := range final.Parts {
		if part.Inquiry != "" {
			prompts++
		}
	}
	require.GreaterOrEqual(t, prompts, 2, "both prompts must survive")
}

type reproNotifier struct{ ch chan rpc.Notification }

func (n *reproNotifier) Notify(method string, params any) error {
	select {
	case n.ch <- rpc.Notification{Method: method, Params: params}:
	default:
	}
	return nil
}

func waitFor(t *testing.T, ch <-chan rpc.Notification, method string, d time.Duration) rpc.Notification {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case n := <-ch:
			if n.Method == method {
				return n
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %s", method)
		}
	}
}

type staticReplyProvider struct{ reply string }

func (p *staticReplyProvider) Name() string        { return "static" }
func (p *staticReplyProvider) Fingerprint() string { return "static/v0" }
func (p *staticReplyProvider) SetModel(string)     {}
func (p *staticReplyProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *staticReplyProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: p.reply})
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent(p.reply)},
		StopReason: message.StopEnd,
	}
	bus.PushFigaro(msg)
	return nil
}
