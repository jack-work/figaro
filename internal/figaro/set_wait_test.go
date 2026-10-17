package figaro_test

// `--wait` is opt-in.

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

// The default path never waits, whatever the aria is doing.
func TestSetDoesNotWaitByDefaultDuringAToolRound(t *testing.T) {
	bt := &blockingSteeringTool{started: make(chan struct{}), release: make(chan struct{})}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_wait", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"before"`)}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil),
		Provider:  prov, Tools: reg, Form: cb,
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
		_, _, _ = a.SetIntent(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"brief": json.RawMessage(`"queued"`),
		}, nil), 0, false)
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

// The opt-in path answers with the writer's verdict.
func TestSetAwaitingAnswersAtTheRoundBoundary(t *testing.T) {
	bt := &blockingSteeringTool{started: make(chan struct{}), release: make(chan struct{})}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_wait2", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"before"`)}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil),
		Provider:  prov, Tools: reg, Form: cb,
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
		_, applied, err := a.SetAwaiting(context.Background(), form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"brief": json.RawMessage(`"awaited"`),
		}, nil), 0, false)
		got <- result{applied, err}
	}()

	// It must answer with the round still in flight: the form has its own actor.
	select {
	case r := <-got:
		require.NoError(t, r.err)
		_, hasK := r.applied.Entry("brief")
		require.True(t, hasK, "the verdict must name what landed")
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

// A cancelled context does not refuse a set: nothing waits.
func TestACancelledContextStillApplies(t *testing.T) {
	cb, _ := form.Open("")
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID:        testID,
		Backend:   testBE,
		Projector: uiir.New(nil),
		Form:      cb,
	})
	defer a.Kill()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, applied, err := a.SetAwaiting(ctx, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"brief": json.RawMessage(`"x"`),
	}, nil), 0, false)
	require.NoError(t, err)
	_, hasK := applied.Entry("brief")
	require.True(t, hasK)
}
