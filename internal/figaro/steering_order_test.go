package figaro_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
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
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		ID:         "steering-order",
		SocketPath: "/tmp/steering-order.sock",
		Provider:   prov,
		Tools:      reg,
		Chalkboard: cb,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "initial")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	submitPrompt(a, "steer one")
	submitPrompt(a, "steer two")
	close(bt.release)
	waitTurnDone(t, frames)

	msgs := a.Context()
	require.Len(t, msgs, 6)
	require.Equal(t, []message.Role{
		message.RoleInput,
		message.RoleOutput,
		message.RoleInput,
		message.RoleInput,
		message.RoleInput,
		message.RoleOutput,
	}, []message.Role{msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role, msgs[4].Role, msgs[5].Role})
	require.Len(t, msgs[2].Content, 1)
	require.Equal(t, message.ContentToolResult, msgs[2].Content[0].Type)
	require.Len(t, msgs[3].Content, 1)
	require.Equal(t, message.ContentProse, msgs[3].Content[0].Type)
	require.Equal(t, "steer one", msgs[3].Content[0].Text)
	require.Len(t, msgs[4].Content, 1)
	require.Equal(t, message.ContentProse, msgs[4].Content[0].Type)
	require.Equal(t, "steer two", msgs[4].Content[0].Text)

	// The canonical order survives, but as THREE turns rather than five
	// message-granular units: each prompt is node 0 of the exchange it began,
	// and the tool round folds into the turn that issued it.
	read := a.Read(aria.Anchor{Turn: 0}, 1<<20)
	require.Len(t, read.Parts, 3)

	var voices [][]string
	for _, part := range read.Parts {
		var v []string
		for _, n := range part.Nodes {
			v = append(v, n.Role)
		}
		voices = append(voices, v)
	}
	require.Equal(t, [][]string{
		{livedoc.RoleInput, livedoc.RoleOutput}, // "initial" + its tool round
		{livedoc.RoleInput},                     // "steer one"
		{livedoc.RoleInput, livedoc.RoleOutput}, // "steer two" + "done"
	}, voices)
	require.Equal(t, int32(2), prov.calls.Load())
}

// TestChalkboardSetDuringToolRoundAppliesNextRound proves a chalkboard patch
// enqueued mid-turn (a model switch) is serviced at the same tool-window
// boundary steering prompts are — so the next provider round already sees the
// new model, rather than the patch waiting out the whole turn.
func TestChalkboardSetDuringToolRoundAppliesNextRound(t *testing.T) {
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
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"before"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		ID:         "midturn-set",
		SocketPath: "/tmp/midturn-set.sock",
		Provider:   prov,
		Tools:      reg,
		Chalkboard: cb,
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
	_, _, err := a.Set(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model": json.RawMessage(`"after"`),
	}})
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
