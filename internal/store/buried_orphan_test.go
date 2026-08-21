package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

func unmatchedInvokes(rows []Entry[message.Message]) []string {
	answered := map[string]bool{}
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult {
				answered[c.ToolCallID] = true
			}
		}
	}
	var open []string
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolInvoke && !answered[c.ToolCallID] {
				open = append(open, c.ToolCallID)
			}
		}
	}
	return open
}

// AN ORPHANED INVOKE BURIED IN HISTORY MUST STILL BE CLOSABLE.
//
// Gluck, 0.28.1: "messages.98: tool_use ids were found without tool_result
// blocks immediately after", on an aria whose tail was past 440. The door
// stops NEW orphans -- it completes a set the moment anything else lands --
// so a buried one can only come from history written before the door
// existed, which every aria upgraded from an older figaro has.
//
// outstandingInvokes scanned BACKWARD FOR THE LAST invoke-bearing message and
// examined only that one. With a well-formed round after it, the buried
// orphan was invisible to the door, to CloseOpenToolCalls, and therefore to
// every repair figaro has -- and the aria could never send again.
func TestABuriedOrphanedInvokeIsClosed(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)

	// Written BENEATH the door, as a figaro that predates it left the history.
	be.mu.Lock()
	h, err := be.handleLocked(aria)
	be.mu.Unlock()
	require.NoError(t, err)
	raw := h.ir
	for _, m := range []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("go")}},
		{Role: message.RoleOutput, Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "toolu_buried", ToolName: "bash"}}},
		// ...and the conversation carried on, well-formed, for hundreds of turns.
		{Role: message.RoleOutput, Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "toolu_later", ToolName: "bash"}}},
		{Role: message.RoleInput, Content: []message.Content{
			message.ToolResultContent("toolu_later", "bash", "done", false)}},
	} {
		_, err := raw.Append(Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	require.Equal(t, []string{"toolu_buried"}, unmatchedInvokes(raw.Read()), "fixture")

	n, err := be.CloseOpenToolCalls(aria)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the repair reported closing nothing")
	require.Empty(t, unmatchedInvokes(raw.Read()),
		"a buried orphan survived the repair, so this aria is refused by every "+
			"provider on every send, forever")
}
