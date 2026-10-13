package figaro

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// buildStream pre-populates an in-memory stream; LTs are assigned by Append in order.
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

func requireRepairTic(t *testing.T, m message.Message, ids ...string) {
	t.Helper()
	require.Equal(t, message.RoleInput, m.Role)
	require.Len(t, m.Content, len(ids))
	for i, id := range ids {
		assert.Equal(t, message.ContentToolResult, m.Content[i].Type)
		assert.Equal(t, id, m.Content[i].ToolCallID)
		assert.True(t, m.Content[i].IsError)
		assert.Contains(t, m.Content[i].Text, tailRepairNotice)
	}
}

func TestTailRepair_EmptyStreamNoOp(t *testing.T) {
	s := buildStream(t)
	repairInterruptedTail(s, "aria")
	assert.Empty(t, s.Read())
}

func TestTailRepair_NonDanglingNoOp(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}},
		message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("salve")}, StopReason: message.StopEnd},
	)
	before := len(s.Read())
	repairInterruptedTail(s, "aria")
	assert.Len(t, s.Read(), before)
}

func TestTailRepair_DanglingToolUseAtTail(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash"), toolCall("tc_b", "read")},
			StopReason: message.StopToolInvoke,
		},
	)
	repairInterruptedTail(s, "aria")
	entries := s.Read()
	require.Len(t, entries, 3)
	requireRepairTic(t, entries[2].Payload, "tc_a", "tc_b")
}

func TestTailRepair_AbortedPartialAtTail(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopAborted,
		},
	)
	repairInterruptedTail(s, "aria")
	entries := s.Read()
	require.Len(t, entries, 3)
	requireRepairTic(t, entries[2].Payload, "tc_a")
}

func TestTailRepair_Idempotent(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopToolInvoke,
		},
	)
	repairInterruptedTail(s, "aria")
	repairInterruptedTail(s, "aria")
	entries := s.Read()
	require.Len(t, entries, 3)
	requireRepairTic(t, entries[2].Payload, "tc_a")
}

func TestTailRepair_WellFormedToolResultNoOp(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopToolInvoke,
		},
		message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{toolResult("tc_a")},
		},
	)
	before := len(s.Read())
	repairInterruptedTail(s, "aria")
	assert.Len(t, s.Read(), before)
}

func TestTailRepair_PartialToolResultsLeavesUnrecoverable(t *testing.T) {
	// A tic covering one of two tool_use blocks is unrecoverable under append-only; left alone.
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run both")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash"), toolCall("tc_b", "read")},
			StopReason: message.StopToolInvoke,
		},
		message.Message{Role: message.RoleInput, Content: []message.Content{toolResult("tc_a")}},
	)
	before := len(s.Read())
	repairInterruptedTail(s, "aria")
	assert.Len(t, s.Read(), before, "no tic appended for unrecoverable trailing entries")
}

func TestTailRepair_NoToolCallsNoOp(t *testing.T) {
	// stop_reason=tool_use with no tool_call blocks is well-formed.
	s := buildStream(t,
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("thinking…")},
			StopReason: message.StopToolInvoke,
		},
	)
	before := len(s.Read())
	repairInterruptedTail(s, "aria")
	assert.Len(t, s.Read(), before)
}

// The repair tic survives a backend reopen.
func TestTailRepair_FileBackedPersists(t *testing.T) {
	dir := t.TempDir()

	b1, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	l, err := b1.CreateOutfit("d", message.Patch{})
	require.NoError(t, err)
	conv, err := b1.CreateConversation(l)
	require.NoError(t, err)
	log1, err := b1.OpenFigIR(conv)
	require.NoError(t, err)
	_, err = log1.Append(store.Entry[message.Message]{
		Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
	})
	require.NoError(t, err)
	_, err = log1.Append(store.Entry[message.Message]{
		Payload: message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_disk", "bash")},
			StopReason: message.StopToolInvoke,
		},
	})
	require.NoError(t, err)
	require.NoError(t, b1.Close())

	b2, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	log2, err := b2.OpenFigIR(conv)
	require.NoError(t, err)
	repairInterruptedTail(log2, conv)
	require.NoError(t, b2.Close())

	b3, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	defer b3.Close()
	log3, err := b3.OpenFigIR(conv)
	require.NoError(t, err)
	entries := log3.Read()
	require.NotEmpty(t, entries)
	requireRepairTic(t, entries[len(entries)-1].Payload, "tc_disk")
}

// A head fork taken mid-turn writes its birth record after the inherited
// tool_use; peeking at the tail alone misses the repair.
func TestTailRepair_DanglingCallBehindABirthRecord(t *testing.T) {
	s := buildStream(t,
		message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("run it")}},
		message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{toolCall("tc_a", "bash")},
			StopReason: message.StopToolInvoke,
		},
		message.Message{Role: message.RoleInput}, // the fork's birth record: no content
	)
	if _, ok := repairInterruptedTail(s, "branch"); !ok {
		t.Fatal("a dangling call behind a birth record went unrepaired")
	}
	entries := s.Read()
	requireRepairTic(t, entries[len(entries)-1].Payload, "tc_a")
	if _, ok := repairInterruptedTail(s, "branch"); ok {
		t.Fatal("repaired twice")
	}
}
