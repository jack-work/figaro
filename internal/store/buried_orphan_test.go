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

// The door is bounded: the IR is correct by construction, so only the last
// round is inspected. A buried orphan from history written before the door
// survives it, and RepairToolCalls heals it once.
func TestTheDoorDoesNotScanHistoryForOrphans(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)

	// Written beneath the door, as a figaro that predates it left the history.
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

	// The door sees the last round, which is well formed, and closes nothing.
	n, err := be.CloseOpenToolCalls(aria)
	require.NoError(t, err)
	require.Zero(t, n, "the door scanned past the current round")
	require.Equal(t, []string{"toolu_buried"}, unmatchedInvokes(raw.Read()))

	// The explicit repair is what heals damage, and it reports what it did.
	fixed, err := be.RepairToolCalls(aria)
	require.NoError(t, err)
	require.Equal(t, 1, fixed, "the repair closed nothing")
	require.Empty(t, unmatchedInvokes(raw.Read()),
		"a buried orphan survived the repair, so this aria is refused by every "+
			"provider on every send, forever")
}
