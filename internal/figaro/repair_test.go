package figaro

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// buildStream constructs an in-memory stream pre-populated with the
// given messages. LTs are assigned by Append in order.
func buildStream(t *testing.T, msgs ...message.Message) store.Log[message.Message] {
	t.Helper()
	s := store.NewMemLog[message.Message]()
	for _, m := range msgs {
		_, err := s.Append(store.Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	return s
}

func toolCall(id, name string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name}
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
			StopReason: message.StopToolInvoke,
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
			StopReason: message.StopToolInvoke,
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
			StopReason: message.StopToolInvoke,
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
			StopReason: message.StopToolInvoke,
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
			StopReason: message.StopToolInvoke,
		},
	)
	before := len(s.Read())
	appendInterruptSentinelIfDangling(s, "aria")
	assert.Len(t, s.Read(), before)
}

// TestAppendSentinel_FileBackedPersists drives the same flow against a
// real FileStream. The dangling state is written to disk; the function
// inserts a sentinel; a fresh OpenFileStream sees it on reload.
func TestAppendSentinel_FileBackedPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s1, err := store.OpenFileLog[message.Message](path)
	require.NoError(t, err)
	_, err = s1.Append(store.Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("run it")}},
	})
	require.NoError(t, err)
	_, err = s1.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{toolCall("tc_disk", "bash")},
			StopReason: message.StopToolInvoke,
		},
	})
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	// Reopen, run repair, close.
	s2, err := store.OpenFileLog[message.Message](path)
	require.NoError(t, err)
	appendInterruptSentinelIfDangling(s2, "aria-disk")
	require.NoError(t, s2.Close())

	// Final reopen sees the sentinel as the tail.
	s3, err := store.OpenFileLog[message.Message](path)
	require.NoError(t, err)
	defer s3.Close()
	entries := s3.Read()
	require.Len(t, entries, 3)
	assert.True(t, message.IsInterruptSentinel(entries[2].Payload))
	assert.Equal(t, []string{"tc_disk"}, message.DanglingToolCallIDs(entries[2].Payload))
}
