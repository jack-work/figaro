package figaro

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// buildStream constructs an in-memory stream pre-populated with the
// given messages. LTs are assigned by Append in order.
func buildStream(t *testing.T, msgs ...message.Message) store.Stream[message.Message] {
	t.Helper()
	s := store.NewMemStream[message.Message]()
	for _, m := range msgs {
		_, err := s.Append(store.Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	return s
}

func toolCall(id, name string) message.Content {
	return message.Content{Type: message.ContentToolCall, ToolCallID: id, ToolName: name}
}

func toolResult(id string) message.Content {
	return message.ToolResultContent(id, "", "ok", false)
}

func TestAppendSentinel_EmptyStreamNoOp(t *testing.T) {
	s := buildStream(t)
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Empty(t, s.Read())
}

func TestAppendSentinel_NonDanglingNoOp(t *testing.T) {
	// Plain user/assistant text turn — no tool_use.
	s := buildStream(t,
		message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hi")}},
		message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("salve")}, StopReason: message.StopEnd},
	)
	before := len(s.Read())
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Len(t, s.Read(), before)
}

func TestAppendSentinel_DanglingToolUseAtTail(t *testing.T) {
	// Assistant ended with tool_use; tool_result tic never landed.
	s := buildStream(t,
		message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{toolCall("tc_a", "bash"), toolCall("tc_b", "read")},
			StopReason: message.StopToolUse,
		},
	)
	appendInterruptSentinelIfDangling(s, "aria")
	entries := s.Read()
	require.Len(t, entries, 3)
	sentinel := entries[2].Payload
	assert.True(t, message.IsInterruptSentinel(sentinel))
	assert.Equal(t, []string{"tc_a", "tc_b"}, message.DanglingToolCallIDs(sentinel))
}

func TestAppendSentinel_Idempotent(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopToolUse,
		},
	)
	appendInterruptSentinelIfDangling(s, "aria")
	appendInterruptSentinelIfDangling(s, "aria")
	// Exactly one sentinel appended; the second call is a no-op.
	entries := s.Read()
	require.Len(t, entries, 3)
	assert.True(t, message.IsInterruptSentinel(entries[2].Payload))
}

func TestAppendSentinel_WellFormedToolResultNoOp(t *testing.T) {
	// Assistant tool_use followed by complete tool_result tic; nothing to do.
	s := buildStream(t,
		message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopToolUse,
		},
		message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{toolResult("tc_a")},
		},
	)
	before := len(s.Read())
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Len(t, s.Read(), before)
}

func TestAppendSentinel_PartialToolResultsLeavesUnrecoverable(t *testing.T) {
	// Assistant emitted two tool_use blocks; the follow-up tic covers
	// only one of them. Under append-only this is unrecoverable: we
	// cannot splice a sentinel between the assistant and the partial
	// tic. The function logs and leaves the stream alone.
	s := buildStream(t,
		message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("run both")}},
		message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{toolCall("tc_a", "bash"), toolCall("tc_b", "read")},
			StopReason: message.StopToolUse,
		},
		message.Message{Role: message.RoleUser, Content: []message.Content{toolResult("tc_a")}},
	)
	before := len(s.Read())
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Len(t, s.Read(), before, "no sentinel appended for unrecoverable trailing entries")
}

func TestAppendSentinel_NoToolCallsNoOp(t *testing.T) {
	// stop_reason=tool_use but the assistant has no tool_call content
	// blocks. Treat as well-formed; nothing to repair.
	s := buildStream(t,
		message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("thinking…")},
			StopReason: message.StopToolUse,
		},
	)
	before := len(s.Read())
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Len(t, s.Read(), before)
}
