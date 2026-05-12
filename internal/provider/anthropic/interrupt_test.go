package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// TestRenderMessage_InterruptSentinel verifies the sentinel becomes a
// user-role nativeMessage with one tool_result block per dangling
// tool_use_id, IsError=true.
func TestRenderMessage_InterruptSentinel(t *testing.T) {
	a := &Anthropic{}
	sentinel := message.NewInterruptSentinel(
		message.InterruptFault,
		"tool execution was interrupted (recovered on reload)",
		[]message.Content{
			{Type: message.ContentToolCall, ToolCallID: "toolu_a", ToolName: "bash"},
			{Type: message.ContentToolCall, ToolCallID: "toolu_b", ToolName: "read"},
		},
	)
	snap := chalkboard.Snapshot{}
	nm, ok := a.renderMessage(sentinel, &snap)
	require.True(t, ok)
	assert.Equal(t, "user", nm.Role)
	require.Len(t, nm.Content, 2)
	assert.Equal(t, "tool_result", nm.Content[0].Type)
	assert.Equal(t, "toolu_a", nm.Content[0].ToolUseID)
	assert.True(t, nm.Content[0].IsError)

	// The Content field on nativeBlock is interface{} carrying nested
	// blocks; assert via the JSON round-trip.
	raw, err := json.Marshal(nm)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"role":"user"`)
	assert.Contains(t, string(raw), `"tool_use_id":"toolu_a"`)
	assert.Contains(t, string(raw), `"tool_use_id":"toolu_b"`)
	assert.Contains(t, string(raw), `"is_error":true`)
	assert.Contains(t, string(raw), "interrupted")
}

func TestRenderMessage_InterruptSentinel_NoBlocksDropped(t *testing.T) {
	a := &Anthropic{}
	sentinel := message.Message{Role: message.RoleSystemInterrupt}
	snap := chalkboard.Snapshot{}
	_, ok := a.renderMessage(sentinel, &snap)
	assert.False(t, ok)
}

func TestRenderMessage_InterruptSentinel_EmptyTextDefaults(t *testing.T) {
	a := &Anthropic{}
	sentinel := message.Message{
		Role: message.RoleSystemInterrupt,
		Content: []message.Content{
			{Type: message.ContentInterrupt, ToolCallID: "toolu_x"},
		},
	}
	snap := chalkboard.Snapshot{}
	nm, ok := a.renderMessage(sentinel, &snap)
	require.True(t, ok)
	raw, err := json.Marshal(nm)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "(tool execution was interrupted)")
}
