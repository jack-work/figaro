package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
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
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
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

	// Both steers were queued in the same tool round, so they drain as one message at one LT.
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

	// A steer joins the turn in flight rather than opening its own. The tool node
	// belongs in the expectation: msgs[2] carries its result.
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

// A prompt that arrives while a turn is running joins that turn as a steer:
// no flag, no declaration, and no turn of its own.
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
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
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

// A form patch enqueued mid-turn is serviced at the tool-window boundary,
// so the next provider round sees it.
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
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"before"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	_, _, err := a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model": json.RawMessage(`"after"`),
	}, nil), 0)
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
