package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// TestRenderMessage_InterruptSentinel verifies the sentinel turns into
// a user-role MessageParam with one tool_result block per dangling
// tool_use_id, IsError=true, satisfying Anthropic's tool_use → tool_result
// pairing rule for an interrupted turn.
func TestRenderMessage_InterruptSentinel(t *testing.T) {
	p := &Provider{}
	sentinel := message.NewInterruptSentinel(
		message.InterruptFault,
		"tool execution was interrupted (recovered on reload)",
		[]message.Content{
			{Type: message.ContentToolCall, ToolCallID: "toolu_a", ToolName: "bash"},
			{Type: message.ContentToolCall, ToolCallID: "toolu_b", ToolName: "read"},
		},
	)
	snap := chalkboard.Snapshot{}
	mp, ok := p.renderMessage(sentinel, &snap)
	require.True(t, ok, "sentinel should produce a MessageParam")

	wire, err := json.Marshal(mp)
	require.NoError(t, err)

	// Decode generic JSON and assert structure: role=user with two
	// tool_result blocks naming our IDs and is_error=true.
	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(wire, &got))
	assert.Equal(t, "user", got.Role)
	require.Len(t, got.Content, 2)
	assert.Equal(t, "tool_result", got.Content[0].Type)
	assert.Equal(t, "toolu_a", got.Content[0].ToolUseID)
	assert.True(t, got.Content[0].IsError)
	assert.Equal(t, "tool_result", got.Content[1].Type)
	assert.Equal(t, "toolu_b", got.Content[1].ToolUseID)
}

func TestRenderMessage_InterruptSentinel_EmptyContentDropped(t *testing.T) {
	p := &Provider{}
	sentinel := message.Message{Role: message.RoleSystemInterrupt}
	snap := chalkboard.Snapshot{}
	_, ok := p.renderMessage(sentinel, &snap)
	assert.False(t, ok, "sentinel with no interrupt blocks should be skipped")
}

func TestRenderMessage_InterruptSentinel_EmptyTextDefaults(t *testing.T) {
	p := &Provider{}
	sentinel := message.Message{
		Role: message.RoleSystemInterrupt,
		Content: []message.Content{
			{Type: message.ContentInterrupt, ToolCallID: "toolu_x"},
		},
	}
	snap := chalkboard.Snapshot{}
	mp, ok := p.renderMessage(sentinel, &snap)
	require.True(t, ok)
	wire, _ := json.Marshal(mp)
	// The default fallback string must appear somewhere in the wire bytes.
	assert.Contains(t, string(wire), "(tool execution was interrupted)")
}
