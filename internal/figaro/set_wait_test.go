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
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
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
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: "nowait-set", SocketPath: "/tmp/nowait-set.sock",
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
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: "wait-set", SocketPath: "/tmp/wait-set.sock",
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

	// It must NOT have answered yet: the round is still in flight.
	select {
	case r := <-got:
		t.Fatalf("SetAwaiting answered mid-round: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}

	close(bt.release)
	select {
	case r := <-got:
		require.NoError(t, r.err)
		require.Contains(t, r.applied.Set, "brief", "the verdict must name what landed")
	case <-time.After(5 * time.Second):
		t.Fatal("SetAwaiting never answered after the round boundary")
	}
	waitTurnDone(t, frames)

	snap := a.Snapshot()
	v := snap.Lookup("brief")
	require.NotNil(t, v)
	require.Equal(t, "awaited", *v)
}

// A caller whose patience runs out stops waiting; the patch is still queued
// and still lands, because what expired is the wait, not the write.
func TestSetAwaitingHonoursItsContext(t *testing.T) {
	cb, _ := form.Open("")
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: "wait-ctx", SocketPath: "/tmp/wait-ctx.sock",
		Form: cb,
	})
	defer a.Kill()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := a.SetAwaiting(ctx, form.Patch{Set: map[string]json.RawMessage{
		"brief": json.RawMessage(`"x"`),
	}}, 0, false)
	require.Error(t, err)
}
