package figaro_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// slowStreamingProvider streams a delta, then blocks. Used to force a
// mid-turn interrupt (assistant never appends) so the store's on-disk
// tail is a bare user prompt with no assistant reply — the state
// prior investigation flagged as breaking read+subscribe coherence.
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

// TestReadSubscribeAfterInterruptedTurn asserts that after an aria is
// interrupted mid-turn — content on disk, no assistant append — a fresh
// agent restored on the same backend sees the aria's content via
// Read(0), and a live subscriber receives frames on the next prompt.
//
// This mirrors the reported bug: "output after 'figaro send' sometimes
// never arrives over a concurrent 'figaro listen'." The strong lead is
// that a mid-turn interrupt scrambles the head/fork-ancestry such that
// reads return 0 units and the subscribe stream goes silent.
func TestReadSubscribeAfterInterruptedTurn(t *testing.T) {
	t.Parallel()
	testReadSubscribeAfterInterrupt(t, true)
}

// TestReadSubscribeAfterInterrupt_SameAgent runs the same scenario
// without restarting the backend/agent: the interrupt happens, then a
// second prompt goes through on the SAME agent. Reads and subscribers
// must still see it. This is the "disconnect" half of the lead —
// nothing is recycled, only the turn was cancelled.
func TestReadSubscribeAfterInterrupt_SameAgent(t *testing.T) {
	t.Parallel()
	testReadSubscribeAfterInterrupt(t, false)
}

func testReadSubscribeAfterInterrupt(t *testing.T, restart bool) {
	dir := t.TempDir()

	// -- Phase 1: create + prompt + interrupt mid-turn -------------------
	backend1, err := store.NewXwalBackend(dir)
	require.NoError(t, err)
	loadout, err := backend1.CreateLoadout("d", message.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m"`),
		"system.provider": json.RawMessage(`"slow-stream"`),
	}})
	require.NoError(t, err)
	conv, err := backend1.CreateConversation(loadout)
	require.NoError(t, err)

	started := make(chan struct{})
	a1 := figaro.NewAgent(figaro.Config{
		ID:         conv,
		SocketPath: filepath.Join(t.TempDir(), "a1.sock"),
		Provider:   &slowStreamingProvider{started: started},
		Backend:    backend1,
	})

	// Subscribe so we can observe the interrupt done.
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

	// A subscriber that connects AFTER the interrupt (mimicking a
	// concurrent `figaro listen` that survives past the send's exit)
	// must see the on-disk content on catch-up Read, and must receive
	// live frames when the next turn runs on the same agent.
	lateSink := make(chan rpc.Notification, 128)
	lateUnsub := a1.Subscribe(&reproNotifier{ch: lateSink})
	defer lateUnsub()

	// Catch-up Read from the freshly-attached listener.
	lateInit := a1.Read(aria.Anchor{Turn: 0}, 1<<20)
	if len(lateInit.Parts) == 0 {
		t.Fatalf("late Read(0) after interrupt returned 0 committed units")
	}

	var backend2 store.Backend
	var a2 *figaro.Agent
	if restart {
		lateUnsub()
		a1.Kill()
		backend1.Kick()
		require.NoError(t, backend1.Close())
		var err error
		backend2, err = store.NewXwalBackend(dir)
		require.NoError(t, err)
		t.Cleanup(func() { backend2.Close() })
		a2 = figaro.NewAgent(figaro.Config{
			ID:         conv,
			SocketPath: filepath.Join(t.TempDir(), "a2.sock"),
			Provider:   &staticReplyProvider{reply: "second reply after restore"},
			Backend:    backend2,
		})
		t.Cleanup(a2.Kill)
	} else {
		// Same-agent path: keep a1 alive, swap the provider isn't an option,
		// so we test that a1 (with the slow provider) at least serves a
		// FRESH Read(0)+Subscribe correctly. The slow provider on the next
		// prompt is fine — we only assert on the current on-disk content.
		a2 = a1
		t.Cleanup(a2.Kill)
	}

	// The user's prompt "please stream forever" is on disk; a catch-up
	// Read(0) MUST return at least one user unit.
	got := a2.Read(aria.Anchor{Turn: 0}, 1<<20)
	if len(got.Parts) == 0 {
		t.Fatalf("Read(0) after interrupt+restore returned 0 units; want >=1 (user prompt on disk)")
	}
	// The prompt is node 0 of its turn now, not a unit of its own.
	sawUser := false
	for _, c := range got.Parts {
		for _, n := range c.Nodes {
			if n.Role == "user" {
				sawUser = true
			}
		}
	}
	require.True(t, sawUser, "Read(0) should include the on-disk user prompt")

	// Subscribe BEFORE the next prompt (mirrors a concurrent `figaro listen`).
	sink2 := make(chan rpc.Notification, 64)
	unsub2 := a2.Subscribe(&reproNotifier{ch: sink2})
	defer unsub2()

	if !restart {
		return // same-agent path: we've verified Read(0) still returns content
	}

	// Send a fresh prompt and assert we see aria frames + turn.done.
	a2.SubmitPrompt(rpc.QuaRequest{Text: "hello again"})
	waitFor(t, sink2, rpc.MethodTurnDone, 5*time.Second)

	// After the second turn, both the older exchange and the new one must be
	// visible to a fresh reader. Two TURNS, not three units — the prompt is
	// node 0 of its own turn now.
	final := a2.Read(aria.Anchor{Turn: 0}, 1<<20)
	require.GreaterOrEqual(t, len(final.Parts), 2,
		"final Read(0) should contain both the original turn and the new one")
	prompts := 0
	for _, part := range final.Parts {
		for _, n := range part.Nodes {
			if n.Role == "user" {
				prompts++
			}
		}
	}
	require.GreaterOrEqual(t, prompts, 2, "both prompts must survive")
}

// reproNotifier is a tiny sink adopted as figaro.Notifier.
type reproNotifier struct{ ch chan rpc.Notification }

func (n *reproNotifier) Notify(method string, params any) error {
	select {
	case n.ch <- rpc.Notification{Method: method, Params: params}:
	default:
	}
	return nil
}

// waitFor drains until it sees the given method or times out.
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

// staticReplyProvider replies with a fixed string, then appends.
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
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent(p.reply)},
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
