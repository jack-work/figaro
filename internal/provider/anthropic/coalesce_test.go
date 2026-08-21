package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
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

// THE API REJECTS CONSECUTIVE SAME-ROLE MESSAGES, and the history legitimately
// contains them: a turn that errors after committing the prompt and before any
// reply leaves two user records in a row. The SDK provider has always merged
// them; the raw one did not, and every new aria is on the raw one.
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

// An already-alternating history must come back BYTE-IDENTICAL: the common
// case pays a role peek and nothing else, and a row nobody merged is a row
// nobody rewrote.
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

// A row that cannot be parsed must not be rewritten, and must not silently
// vanish into its neighbour.
func TestAnUnreadableRowIsLeftAlone(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"ok"}]}`),
		json.RawMessage(`{"role":"user","content":"not an array"}`),
	}
	got, _ := coalesceRows(rows, []uint64{1, 2})
	require.Len(t, got, 2, "an unparseable row stays its own message rather than being dropped")
	require.Equal(t, string(rows[1]), string(got[1]))
}

// AND END TO END: the assembler must not put two consecutive user messages on
// the wire, which is what it did before this.
func TestTheAssemblerNeverEmitsTwoConsecutiveUserMessages(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first prompt")}},
		// the turn errored here: no assistant record was ever committed
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
