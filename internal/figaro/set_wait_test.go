package figaro_test

// `--wait` is OPT-IN, and this is the pair of tests that keeps it that way.
//
// The first attempt at closing this gap made Set synchronous for everyone and
// hung TestFormSetDuringToolRoundAppliesNextRound for its whole timeout: a set
// arriving mid-turn is applied at the next ROUND BOUNDARY by design, so a
// caller that waits waits for a tool round. That is the right trade only when
// the caller chose it.

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/message"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// The default path must NOT wait, whatever the aria is doing. This is the
// deadlock the first attempt shipped, asserted as an absence.
func TestSetDoesNotWaitByDefaultDuringAToolRound(t *testing.T) {
	bt := &blockingSteeringTool{started: make(chan struct{}), release: make(chan struct{})}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_wait", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{"system.model": json.RawMessage(`"before"`)}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil), SocketPath: "/tmp/nowait-set.sock",
		Provider: prov, Tools: reg, Form: cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}

	done := make(chan struct{})
	go func() {
		_, _, _ = a.SetIntent(form.Patch{Set: map[string]json.RawMessage{
			"brief": json.RawMessage(`"queued"`),
		}}, 0, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("the DEFAULT set blocked on a tool round: waiting must be opt-in")
	}
	close(bt.release)
	waitTurnDone(t, frames)
}

// And the opt-in path answers with the writer's verdict once the round
// boundary is reached, rather than with "queued".
func TestSetAwaitingAnswersAtTheRoundBoundary(t *testing.T) {
	bt := &blockingSteeringTool{started: make(chan struct{}), release: make(chan struct{})}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_wait2", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{"system.model": json.RawMessage(`"before"`)}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil), SocketPath: "/tmp/wait-set.sock",
		Provider: prov, Tools: reg, Form: cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}

	type result struct {
		applied form.Patch
		err     error
	}
	got := make(chan result, 1)
	go func() {
		_, applied, err := a.SetAwaiting(context.Background(), form.Patch{Set: map[string]json.RawMessage{
			"brief": json.RawMessage(`"awaited"`),
		}}, 0, false)
		got <- result{applied, err}
	}()

	// IT MUST ANSWER NOW, WITH THE ROUND STILL IN FLIGHT. This assertion is
	// the inverse of the one it replaces. A set used to ride the figaro's
	// inbox and be applied at a round boundary, so a caller waiting for the
	// verdict waited for the tool; the form has its own actor and the figaro
	// is no longer between them.
	select {
	case r := <-got:
		require.NoError(t, r.err)
		require.Contains(t, r.applied.Set, "brief", "the verdict must name what landed")
	case <-time.After(2 * time.Second):
		t.Fatal("a set waited for the round to end: the figaro is still in the way")
	}

	close(bt.release)
	waitTurnDone(t, frames)

	snap := a.Snapshot()
	v := snap.Lookup("brief")
	require.NotNil(t, v)
	require.Equal(t, "awaited", *v)
}

// A CANCELLED CONTEXT NO LONGER REFUSES A SET, because nothing waits. The
// write is applied by the form's actor before the call returns, so there is
// no window in which a caller's patience can expire -- and refusing here
// would lose a write for a reason that no longer exists.
func TestACancelledContextStillApplies(t *testing.T) {
	cb, _ := form.Open("")
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil), SocketPath: "/tmp/wait-ctx.sock",
		Form: cb,
	})
	defer a.Kill()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, applied, err := a.SetAwaiting(ctx, form.Patch{Set: map[string]json.RawMessage{
		"brief": json.RawMessage(`"x"`),
	}}, 0, false)
	require.NoError(t, err)
	require.Contains(t, applied.Set, "brief")
}
