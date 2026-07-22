package figaro_test

import (
	"context"
	"encoding/json"
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
		message.RoleUser,
		message.RoleAssistant,
		message.RoleUser,
		message.RoleUser,
		message.RoleUser,
		message.RoleAssistant,
	}, []message.Role{msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role, msgs[4].Role, msgs[5].Role})
	require.Len(t, msgs[2].Content, 1)
	require.Equal(t, message.ContentToolResult, msgs[2].Content[0].Type)
	require.Len(t, msgs[3].Content, 1)
	require.Equal(t, message.ContentProse, msgs[3].Content[0].Type)
	require.Equal(t, "steer one", msgs[3].Content[0].Text)
	require.Len(t, msgs[4].Content, 1)
	require.Equal(t, message.ContentProse, msgs[4].Content[0].Type)
	require.Equal(t, "steer two", msgs[4].Content[0].Text)

	read := a.Read(0)
	require.Len(t, read.Committed, 5)
	require.Equal(t, []string{"user", "assistant", "user", "user", "assistant"}, []string{
		read.Committed[0].Role,
		read.Committed[1].Role,
		read.Committed[2].Role,
		read.Committed[3].Role,
		read.Committed[4].Role,
	})
	require.Equal(t, int32(2), prov.calls.Load())
}
