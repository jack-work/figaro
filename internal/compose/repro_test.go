package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

// drive feeds a sequence of frames (each a full turn message set, as
// composeTurn builds them) through the real compose→server→client pipeline as
// one growing live unit, and returns the client's final open-unit nodes,
// each labeled by (type, content) so a duplicated logical block is visible.
func drive(frames [][]message.Message) []string {
	srv := aria.NewServer()
	cli := aria.NewClient()
	srv.Subscribe(func(r aria.AriaRead) { cli.Apply(r) })
	srv.Open(1, "assistant")
	for _, msgs := range frames {
		srv.Update(Nodes(msgs, nil))
	}
	var out []string
	if v := cli.View().Open; v != nil {
		for _, n := range v.Nodes {
			label := string(n.Type) + ":" + n.Markdown
			if n.Type == "tool" {
				label += n.Name + "#" + n.ID // tools distinguished by their call id
			}
			out = append(out, label)
		}
	}
	return out
}

// assertNoDup fails if any (type,content) label appears more than once.
func assertNoDup(t *testing.T, tag string, nodes []string) {
	t.Helper()
	seen := map[string]int{}
	for _, n := range nodes {
		seen[n]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("[%s] DUPLICATED %q x%d — full: %v", tag, n, c, nodes)
		}
	}
	t.Logf("[%s] nodes: %v", tag, nodes)
}

// helpers: messages carry a stable LogicalTime (as composeTurn assigns from the
// fig IR), constant across the frames that evolve the same message.
func asstLT(lt uint64, cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleAssistant, Content: cs, LogicalTime: lt}
}
func think(s string) message.Content { return message.Content{Type: message.ContentThinking, Text: s} }
func prose(s string) message.Content { return message.Content{Type: message.ContentProse, Text: s} }
func tool(id, name string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name}
}
func resLT(lt uint64, id, out string) message.Message {
	return message.Message{Role: message.RoleUser, LogicalTime: lt,
		Content: []message.Content{{Type: message.ContentToolResult, ToolCallID: id, Text: out}}}
}

// Natural forward stream of a plaid-shaped 2-round turn. Must never dup
// (regression guard both before and after the fix).
func TestRepro_A_NaturalForward(t *testing.T) {
	a1 := func(extra ...message.Content) message.Message {
		return asstLT(1, append([]message.Content{think("checking the CLI"), tool("A", "transactions")}, extra...)...)
	}
	a2 := func(cs ...message.Content) message.Message { return asstLT(3, cs...) }
	frames := [][]message.Message{
		{asstLT(1, think("checking the CLI"))},
		{a1()},
		{a1(), resLT(2, "A", "help")},
		{a1(), resLT(2, "A", "help"), a2(think("verifying read-only"))},
		{a1(), resLT(2, "A", "help"), a2(think("verifying read-only"), tool("B", "balance"))},
	}
	assertNoDup(t, "A natural", drive(frames))
}

// An earlier block fills in after a later thinking already exists, shifting the
// thinking's flattened position while a tool holds an explicit id. This is the
// core positional-id duplication — FAILS before the fix, must PASS after.
func TestRepro_B_EmptyEarlierFills(t *testing.T) {
	frames := [][]message.Message{
		{asstLT(1, prose(""), tool("A", "transactions"), think("verifying read-only"))},
		{asstLT(1, prose("Here is the plan"), tool("A", "transactions"), think("verifying read-only"))},
	}
	assertNoDup(t, "B empty-fill", drive(frames))
}

// A block un-fills mid-stream, shrinking the list and orphaning a higher
// positional id — FAILS before the fix, must PASS after.
func TestRepro_C_BlockUnfills(t *testing.T) {
	frames := [][]message.Message{
		{asstLT(1, think("alpha"), prose("beta"), think("gamma"))},
		{asstLT(1, think("alpha"), prose(""), think("gamma"))},
	}
	assertNoDup(t, "C un-fill", drive(frames))
}
