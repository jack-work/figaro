package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func rolesOf(t *testing.T, rows []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowRole(r))
	}
	return out
}

// A turn that errors after committing the prompt leaves two user records;
// the API rejects consecutive same-role messages.
func TestAdjacentSameRoleRowsAreMergedForTheWire(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"first prompt"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"second prompt"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"reply"}]}`),
	}
	got, lts := coalesceRows(rows, []uint64{1, 2, 3})

	require.Equal(t, []string{"user", "assistant"}, rolesOf(t, got),
		"two user rows in a row is a malformed request")
	require.Equal(t, []uint64{2, 3}, lts, "the merged row keeps the LATER LT, which is what a per-LT tag targets")

	var m nativeMessage
	require.NoError(t, json.Unmarshal(got[0], &m))
	require.Len(t, m.Content, 2, "merging must keep BOTH messages' content, not drop one")
	require.Equal(t, "first prompt", m.Content[0].Text)
	require.Equal(t, "second prompt", m.Content[1].Text)
}

func TestAnAlternatingHistoryIsUntouched(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage("{\n  \"role\": \"user\",\n  \"content\": []\n}"),
		json.RawMessage(`{"role":"assistant","content":[]}`),
	}
	got, lts := coalesceRows(rows, []uint64{1, 2})
	require.Equal(t, []uint64{1, 2}, lts)
	for i := range rows {
		require.Equal(t, string(rows[i]), string(got[i]),
			"an untouched row must not be re-encoded: whitespace and key order are the wire bytes")
	}
}

func TestAnUnreadableRowIsLeftAlone(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"ok"}]}`),
		json.RawMessage(`{"role":"user","content":"not an array"}`),
	}
	got, _ := coalesceRows(rows, []uint64{1, 2})
	require.Len(t, got, 2, "an unparseable row stays its own message rather than being dropped")
	require.Equal(t, string(rows[1]), string(got[1]))
}

func TestTheAssemblerNeverEmitsTwoConsecutiveUserMessages(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first prompt")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("second prompt")}},
	}
	snap := systemSnapshot(t, "you are a test agent")
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)

	roles := rolesOf(t, req.Messages)
	for i := 1; i < len(roles); i++ {
		require.NotEqual(t, roles[i-1], roles[i],
			"roles must alternate: the API refuses %v", roles)
	}
}

var _ = provider.MaxCacheBreakpoints

// A closing notice written twice: the IR keeps both, the wire carries one.
func TestADuplicateToolResultNeverReachesTheWire(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"X","name":"bash","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"X","content":"closed"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"X","content":"closed again"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"carry on"}]}`),
	}
	got := dropDuplicateResults(rows)

	results := 0
	for _, raw := range got {
		var m nativeMessage
		require.NoError(t, json.Unmarshal(raw, &m))
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolUseID == "X" {
				results++
			}
		}
	}
	require.Equal(t, 1, results, "the wire carries %d results for one call", results)
	require.Len(t, got, 3, "a message left with no blocks must be dropped, not sent empty")
}

// A cached assistant row with no tool_use, followed by the seal's closing
// result: the orphan result must not reach the wire.
func TestAnOrphanToolResultNeverReachesTheWire(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"running it"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"GONE","is_error":true,"content":"closed without a result"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"carry on"}]}`),
	}
	got := pairToolCalls(rows)
	require.Equal(t, []string{"assistant", "user"}, rolesOf(t, got),
		"the orphan's message held nothing else, so it goes entirely")
	require.NotContains(t, string(got[1]), "tool_result")
}

func TestAnUnansweredToolCallIsClosedOnTheWire(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"X","name":"bash","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"never mind"}]}`),
	}
	got := pairToolCalls(rows)
	require.Equal(t, []string{"assistant", "user", "user"}, rolesOf(t, got))
	require.Contains(t, string(got[1]), `"tool_use_id":"X"`)
	require.Contains(t, string(got[1]), `"is_error":true`)

	tail := pairToolCalls(rows[:1])
	require.Len(t, tail, 2)
	require.Contains(t, string(tail[1]), `"tool_use_id":"X"`)
}

// A parallel batch's results arrive as separate IR records; the gate must
// pair them against the coalesced message.
func TestAResultInTheNextRecordIsStillTheAnswer(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"X","name":"bash","input":{}},{"type":"tool_use","id":"Y","name":"bash","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"X","content":"actual X"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"Y","content":"actual Y"}]}`),
	}
	got, _ := provider.CollectRows(coalesceRowsSeq(pairToolCallsSeq(dropDuplicateResultsSeq(provider.SliceRows(rows, nil)))))

	require.Equal(t, []string{"assistant", "user"}, rolesOf(t, got),
		"the wire takes one assistant message and one user message answering it")
	wire := string(got[1])
	require.Contains(t, wire, "actual X")
	require.Contains(t, wire, "actual Y", "the real result was dropped as an orphan")
	require.NotContains(t, wire, unclosedCallNotice, "a call that was answered was closed as unanswered")
	require.Less(t, strings.Index(wire, "actual X"), strings.Index(wire, "actual Y"))
}

func TestAPairedHistoryIsUntouched(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage("{\n  \"role\": \"assistant\",\n  \"content\": [{\"type\": \"tool_use\", \"id\": \"X\", \"name\": \"bash\", \"input\": {}}]\n}"),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"X","content":"ok"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"done"}]}`),
	}
	got := pairToolCalls(rows)
	require.Len(t, got, 3)
	for i := range rows {
		require.Equal(t, string(rows[i]), string(got[i]))
	}
}

func TestAHistoryWithoutDuplicatesIsUntouched(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage("{\n  \"role\": \"user\",\n  \"content\": []\n}"),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"X"}]}`),
	}
	got := dropDuplicateResults(rows)
	require.Len(t, got, 2)
	for i := range rows {
		require.Equal(t, string(rows[i]), string(got[i]))
	}
}

func coalesceRows(rows []json.RawMessage, lts []uint64) ([]json.RawMessage, []uint64) {
	return provider.CollectRows(coalesceRowsSeq(provider.SliceRows(rows, lts)))
}

func dropDuplicateResults(rows []json.RawMessage) []json.RawMessage {
	out, _ := provider.CollectRows(dropDuplicateResultsSeq(provider.SliceRows(rows, nil)))
	return out
}

func pairToolCalls(rows []json.RawMessage) []json.RawMessage {
	out, _ := provider.CollectRows(pairToolCallsSeq(provider.SliceRows(rows, nil)))
	return out
}
