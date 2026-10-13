package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	client "github.com/jack-work/figaro/sdk/aria"
)

// drive runs frames through compose, server and client and returns the
// client's open-turn nodes labeled by (type, content).
func drive(frames [][]message.Message) []string {
	srv := aria.NewServer()
	cli := client.NewClient()
	srv.Subscribe(func(r aria.Page) { cli.Apply(r, client.Notify) })
	srv.OpenTurn(uint64(1))
	for _, msgs := range frames {
		srv.Update(nil, Nodes(msgs, nil, nil, nil), 0)
	}
	var out []string
	if v := cli.View().Open; v != nil {
		for _, n := range v.Nodes {
			label := string(n.Type) + ":" + n.Markdown
			if n.Type == "tool" {
				label += n.Name + "#" + n.ID
			}
			out = append(out, label)
		}
	}
	return out
}

func assertNoDup(t *testing.T, tag string, nodes []string) {
	t.Helper()
	seen := map[string]int{}
	for _, n := range nodes {
		seen[n]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("[%s] DUPLICATED %q x%d: full: %v", tag, n, c, nodes)
		}
	}
	t.Logf("[%s] nodes: %v", tag, nodes)
}

func asstLT(lt uint64, cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleOutput, Content: cs, LogicalTime: lt}
}
func think(s string) message.Content { return message.Content{Type: message.ContentThinking, Text: s} }
func prose(s string) message.Content { return message.Content{Type: message.ContentProse, Text: s} }
func tool(id, name string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name}
}
func resLT(lt uint64, id, out string) message.Message {
	return message.Message{Role: message.RoleInput, LogicalTime: lt,
		Content: []message.Content{{Type: message.ContentToolResult, ToolCallID: id, Text: out}}}
}

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

// An earlier block fills in after a later thinking already exists.
func TestRepro_B_EmptyEarlierFills(t *testing.T) {
	frames := [][]message.Message{
		{asstLT(1, prose(""), tool("A", "transactions"), think("verifying read-only"))},
		{asstLT(1, prose("Here is the plan"), tool("A", "transactions"), think("verifying read-only"))},
	}
	assertNoDup(t, "B empty-fill", drive(frames))
}

// A block un-fills mid-stream.
func TestRepro_C_BlockUnfills(t *testing.T) {
	frames := [][]message.Message{
		{asstLT(1, think("alpha"), prose("beta"), think("gamma"))},
		{asstLT(1, think("alpha"), prose(""), think("gamma"))},
	}
	assertNoDup(t, "C un-fill", drive(frames))
}

// At seal, a decode that keeps an empty thinking block the assembler omitted
// must not shift the node after it.
func TestRepro_D_SealEmptyThinkingChurn(t *testing.T) {
	inflight := asstLT(1, tool("A", "bash"), think("real reasoning"))
	sealedOldDecode := asstLT(1, think(""), tool("A", "bash"), think("real reasoning"))
	sealedFixedDecode := asstLT(1, tool("A", "bash"), think("real reasoning"))

	assertNoDup(t, "D old-decode", drive([][]message.Message{{inflight}, {sealedOldDecode}}))
	assertNoDup(t, "D fixed-decode", drive([][]message.Message{{inflight}, {sealedFixedDecode}}))
}
