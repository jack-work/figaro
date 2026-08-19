package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

type blockingSteeringTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *blockingSteeringTool) Name() string        { return "steer" }
func (t *blockingSteeringTool) Description() string { return "blocks until the test releases it" }
func (t *blockingSteeringTool) Parameters() any     { return map[string]any{} }
func (t *blockingSteeringTool) Execute(
	ctx context.Context,
	_ map[string]any,
	_ tool.OnOutput,
) ([]message.Content, error) {
	close(t.started)
	select {
	case <-t.release:
		return []message.Content{message.TextContent("tool done")}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestPromptDuringToolRoundKeepsCanonicalOrder(t *testing.T) {
	bt := &blockingSteeringTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools: []specTool{{
			id:      "tc_steer",
			name:    "steer",
			args:    map[string]interface{}{},
			readyAt: 0,
		}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/steering-order.sock",
		Provider:   prov,
		Tools:      reg,
		Form:       cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	submitSteer(a, "steer one")
	submitSteer(a, "steer two")
	close(bt.release)
	waitTurnDone(t, frames)

	// A DRAINED BATCH IS ONE MESSAGE. Both steers were queued during the same
	// tool round, so TakeReadyUserPrompts lifts them together and the drain
	// joins them with a newline: one message, one LT, one steering decision -
	// not two messages for the model to reconcile.
	msgs := conversationOnly(a.Context())
	require.Len(t, msgs, 5)
	require.Equal(t, []message.Role{
		message.RoleInput,
		message.RoleOutput,
		message.RoleInput,
		message.RoleInput,
		message.RoleOutput,
	}, []message.Role{msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role, msgs[4].Role})
	require.Len(t, msgs[2].Content, 1)
	require.Equal(t, message.ContentToolResult, msgs[2].Content[0].Type)
	require.Len(t, msgs[3].Content, 1)
	require.Equal(t, message.ContentProse, msgs[3].Content[0].Type)
	require.Equal(t, "steer one\n\nsteer two", msgs[3].Content[0].Text)
	require.True(t, msgs[3].Steering, "a prompt drained mid-turn is classified at the drain")

	// The canonical order survives as ONE turn. A steer is a direction aimed at
	// the exchange already in flight, so it joins that turn rather than opening
	// its own: which is what previously truncated the turn being steered and
	// left its closing prose unrendered.
	//
	// The TOOL node belongs here too. This expectation used to omit it, which
	// pinned a real defect: the assertions above prove a tool ran (msgs[1] is the
	// assistant, msgs[2] carries its ContentToolResult), so a projection without a
	// tool node was claiming that a tool present in the IR renders nowhere. It
	// was overwritten in place when the recomposed window narrowed after a drain.
	read := a.Read(aria.Anchor{Turn: 0}, 1<<20)
	for _, part := range read.Parts {
		t.Logf("turn=%d inquiry=%q nodes=%d", part.ID, part.Inquiry, len(part.Nodes))
		for _, n := range part.Nodes {
			t.Logf("    %s/%s %q", n.Type, n.Role, n.Markdown)
		}
	}
	require.Len(t, read.Parts, 1)
	require.Equal(t, "initial", read.Parts[0].Inquiry)

	var kinds []string
	for _, n := range read.Parts[0].Nodes {
		kinds = append(kinds, string(n.Type))
	}
	require.Equal(t, []string{"tool", "steering", "prose"}, kinds,
		"the drained batch is ONE steering node inside the turn it steers, "+
			"and the tool call it interrupted must survive")
	require.Equal(t, "steer one\n\nsteer two", read.Parts[0].Nodes[1].Markdown,
		"both queued texts survive, separated by a BLANK line so markdown keeps "+
			"them on separate rows: nothing is dropped and nothing is rejoined")
	require.Equal(t, int32(2), prov.calls.Load())
}

// TIMING IS THE WHOLE RULE: a prompt that arrives while a turn is running joins
// that turn as a steering aside. There is no flag and no declaration: one
// command, identical whether or not the aria is busy.
//
// This test previously asserted the opposite, from an era when intent was
// CARRIED. That design existed to avoid demoting a genuine question, but it
// required the caller to know something the caller cannot reliably know, and it
// put a second classification point outside the drain. The rule now: a message
// sent while figaro is working IS a direction to the work in progress. A steer
// is not a turn, so it does not get one.
func TestMidTurnPromptJoinsTheRunningTurn(t *testing.T) {
	bt := &blockingSteeringTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_steer", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/unmarked-midturn.sock",
		Provider:   prov,
		Tools:      reg,
		Form:       cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	submitPrompt(a, "a mid-turn nudge")
	close(bt.release)
	waitTurnDone(t, frames)

	read := a.Read(aria.Anchor{Turn: 0}, 1<<20)
	for _, part := range read.Parts {
		t.Logf("turn=%d inquiry=%q nodes=%d", part.ID, part.Inquiry, len(part.Nodes))
	}
	require.Len(t, read.Parts, 1, "a prompt drained mid-turn joins the running turn")
	require.Equal(t, "initial", read.Parts[0].Inquiry)
	var sawSteering bool
	for _, n := range read.Parts[0].Nodes {
		if n.Type == livedoc.NodeSteering {
			sawSteering = true
		}
	}
	require.True(t, sawSteering,
		"the mid-turn prompt must render as a steering node inside that turn")
	require.Equal(t, int32(2), prov.calls.Load())
}

// TestFormSetDuringToolRoundAppliesNextRound proves a form patch
// enqueued mid-turn (a model switch) is serviced at the same tool-window
// boundary steering prompts are: so the next provider round already sees the
// new model, rather than the patch waiting out the whole turn.
func TestFormSetDuringToolRoundAppliesNextRound(t *testing.T) {
	bt := &blockingSteeringTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools: []specTool{{
			id:      "tc_steer",
			name:    "steer",
			args:    map[string]interface{}{},
			readyAt: 0,
		}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"before"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/midturn-set.sock",
		Provider:   prov,
		Tools:      reg,
		Form:       cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	// Switch the model while the tool round is in flight.
	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.model": json.RawMessage(`"after"`),
	}}, 0)
	require.NoError(t, err)
	close(bt.release)
	waitTurnDone(t, frames)

	prov.modelMu.Lock()
	models := append([]string(nil), prov.models...)
	prov.modelMu.Unlock()
	require.Equal(t, []string{"before", "after"}, models,
		"round 1 runs on the original model; the mid-turn set must land before round 2")

	snap := a.Snapshot()
	v := snap.Lookup("system.model")
	require.NotNil(t, v)
	require.Equal(t, "after", *v)
}
