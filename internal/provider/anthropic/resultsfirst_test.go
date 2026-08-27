package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

// A FORK TAKEN WHILE THE PARENT'S TOOLS ARE STILL RUNNING.
//
// These are aria 45c24dda's own rows, as they sit in its cache: the branch's
// fork notice is a record of its own and lands BETWEEN the invoke and the
// results. Coalescing merges the notice with the results into one user turn,
// and the notice was in front, which the API refuses -- forever, because the
// malformed pair replays on every later request.
func TestForkNoticeDoesNotDisplaceTheResults(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"please do a bunch of sleep heavy readonly bash work"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"Un momento"},{"type":"tool_use","id":"toolu_A","name":"bash","input":{"command":"sleep 1"}},{"type":"tool_use","id":"toolu_B","name":"bash","input":{"command":"sleep 2"}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"\u003csystem-reminder name=\"fork\"\u003e{\"forked_from\":\"6146bd53\"}\u003c/system-reminder\u003e"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":[{"type":"text","text":"ok"}]},{"type":"tool_result","tool_use_id":"toolu_B","content":[{"type":"text","text":"ok"}]},{"type":"text","text":"\u003csystem-reminder name=\"aria_id\"\u003e45c24dda\u003c/system-reminder\u003e"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"test"}]}`),
	}
	lts := []uint64{4, 5, 6, 7, 8}

	out, _ := provider.CollectRows(coalesceRowsSeq(dropDuplicateResultsSeq(provider.SliceRows(rows, lts))))
	require.Len(t, out, 3, "the three user rows after the invoke are one turn")

	var invoked, answered nativeMessage
	require.NoError(t, json.Unmarshal(out[1], &invoked))
	require.NoError(t, json.Unmarshal(out[2], &answered))
	require.Equal(t, "assistant", invoked.Role)
	require.Equal(t, "user", answered.Role)

	// THE POSITIONAL RULE: every result leads, and it answers a call the
	// message before it opened.
	open := map[string]bool{}
	for _, b := range invoked.Content {
		if b.Type == "tool_use" {
			open[b.ID] = true
		}
	}
	require.Len(t, open, 2)
	for i, b := range answered.Content {
		if i < len(open) {
			require.Equal(t, "tool_result", b.Type, "block %d must be a result: %s", i, out[2])
			assert.True(t, open[b.ToolUseID], "result %d answers a call that was never opened", i)
			continue
		}
		assert.NotEqual(t, "tool_result", b.Type, "a result trails a text block: %s", out[2])
	}

	// The fork notice is not lost, it merely stands behind the results.
	assert.Contains(t, string(out[2]), `name=\"fork\"`)
	assert.Contains(t, string(out[2]), "test")
}

// The door appends a closing result to the message it is handed, so a message
// that opens with prose keeps its prose in front of the result. The renderer
// puts the result back where the API wants it.
func TestRenderedResultsLeadTheirTurn(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	snap := form.Snapshot{}
	msg, ok := a.renderMessage(message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.TextContent("carry on"),
			message.ToolResultContent("toolu_A", "bash", "closed", true),
		},
	}, &snap)
	require.True(t, ok)
	require.Len(t, msg.Content, 2)
	assert.Equal(t, "tool_result", msg.Content[0].Type)
	assert.Equal(t, "text", msg.Content[1].Type)
}

// Hoisting is STABLE: a result set keeps its call order, and what follows
// keeps the order it was written in.
func TestResultsFirstIsStable(t *testing.T) {
	got := resultsFirst([]nativeBlock{
		{Type: "text", Text: "one"},
		{Type: "tool_result", ToolUseID: "a"},
		{Type: "text", Text: "two"},
		{Type: "tool_result", ToolUseID: "b"},
	})
	require.Len(t, got, 4)
	assert.Equal(t, []string{"a", "b"}, []string{got[0].ToolUseID, got[1].ToolUseID})
	assert.Equal(t, []string{"one", "two"}, []string{got[2].Text, got[3].Text})

	// A turn already in order is handed back untouched.
	already := []nativeBlock{{Type: "tool_result", ToolUseID: "a"}, {Type: "text", Text: "one"}}
	assert.Equal(t, already, resultsFirst(already))
}
