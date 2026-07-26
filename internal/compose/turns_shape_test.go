package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

func userPrompt(text string) message.Message {
	return message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}
}

// turnText flattens a turn's nodes to one string for assertions.
func turnText(t aria.Turn) string {
	var b strings.Builder
	for _, n := range t.Nodes {
		if n.Type == livedoc.NodeTool {
			b.WriteString(n.Name + " " + n.Output + "\n")
		} else {
			b.WriteString(n.Markdown + "\n")
		}
	}
	return b.String()
}

func types(t aria.Turn) []livedoc.NodeType {
	var out []livedoc.NodeType
	for _, n := range t.Nodes {
		out = append(out, n.Type)
	}
	return out
}

// A turn is one exchange: the question that opened it is Turn.Inquiry — text,
// not a unit and not a node — and Nodes holds only what came back.
func TestTurns_InquiryIsTextAndNodesAreTheReply(t *testing.T) {
	msgs := []message.Message{
		userPrompt("first question"),
		assistant(message.TextContent("first answer")),
		userPrompt("second question"),
		assistant(invoke("t1", "bash", "echo hi")),
		toolResultTic(result("t1", "bash", "hi", false)),
		assistant(message.TextContent("second answer")),
	}
	turns := Turns(msgs, nil, nil)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	for i, want := range []string{"first question", "second question"} {
		if turns[i].Inquiry != want {
			t.Errorf("turn %d inquiry = %q, want %q", i, turns[i].Inquiry, want)
		}
		if turns[i].Nodes[0].Role == roleInput {
			t.Errorf("turn %d node 0 speaks in the user's voice; the question is not a node", i)
		}
	}
	if got := turns[0].Nodes[0].Markdown; got != "first answer" {
		t.Errorf("turn 0 node 0 = %q, want the reply", got)
	}
	if turns[0].ID != 1 || turns[1].ID != 2 {
		t.Errorf("turn ids = %d,%d want 1,2", turns[0].ID, turns[1].ID)
	}
	// The second turn folds invoke + result into one tool node.
	if got := turnText(turns[1]); !strings.Contains(got, "bash") || !strings.Contains(got, "hi") {
		t.Errorf("turn 1 should carry the folded tool output:\n%s", got)
	}
}

func TestTurns_SkipsControlOnlyTics(t *testing.T) {
	control := message.Message{Role: message.RoleInput}
	turns := Turns([]message.Message{
		control,
		userPrompt("hello"),
		assistant(message.TextContent("hi there")),
	}, nil, nil)
	if len(turns) != 1 {
		t.Fatalf("control tic should not open a turn; got %+v", turns)
	}
}

// Ordering: a tool node spanning [invokeLT, resultLT] comes BEFORE a steering
// node that shares the result LT.
func TestTurns_ToolBeforeSteeringSharingItsLT(t *testing.T) {
	msgs := []message.Message{
		userPrompt("hello"),
		{Role: message.RoleOutput, Content: []message.Content{
			{Type: message.ContentProse, Text: "hey"},
			{Type: message.ContentToolInvoke, ToolCallID: "t1", ToolName: "test",
				Arguments: map[string]interface{}{"x": 1}},
		}},
		{Role: message.RoleInput, Content: []message.Content{
			message.ToolResultContent("t1", "test", "result-out", false),
			{Type: message.ContentProse, Text: "oh and by the way"}, // the steer
		}},
		{Role: message.RoleOutput, Content: []message.Content{
			{Type: message.ContentProse, Text: "oh cool sure"},
		}},
	}
	for i := range msgs {
		msgs[i].LogicalTime = uint64(60 + i) // 60 prompt, 61 asst, 62 results, 63 asst
	}
	turns := Turns(msgs, nil, nil)
	if len(turns) != 1 {
		t.Fatalf("steering must not open a turn; got %d: %+v", len(turns), turns)
	}
	tn := turns[0]
	want := []livedoc.NodeType{
		livedoc.NodeProse,    // "hey" — node 0 is the agent's first block
		livedoc.NodeTool,     // t1
		livedoc.NodeSteering, // the steer
		livedoc.NodeProse,    // "oh cool sure"
	}
	got := types(tn)
	if len(got) != len(want) {
		t.Fatalf("node types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("node[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// The tool spans invoke and result; the steer carries only the result LT.
	if gotLTs := tn.Nodes[1].LTs; len(gotLTs) != 2 || gotLTs[0] != 61 || gotLTs[1] != 62 {
		t.Errorf("tool node lts = %v, want [61 62]", gotLTs)
	}
	if gotLTs := tn.Nodes[2].LTs; len(gotLTs) != 1 || gotLTs[0] != 62 {
		t.Errorf("steering node lts = %v, want [62]", gotLTs)
	}
	// Two coordinates, at different block indices in different messages.
	if src := tn.Nodes[1].Src; len(src) != 2 || src[0].Block != 1 || src[1].Block != 0 {
		t.Errorf("tool node src = %+v, want invoke 61.1 then result 62.0", src)
	}
	if tn.Nodes[2].Role != roleInput {
		t.Errorf("steering role = %q, want user (the one input-voice node left)", tn.Nodes[2].Role)
	}
	if tn.Nodes[1].ToolCallID != "t1" || tn.Nodes[1].ID != "t1" {
		t.Errorf("tool identity/receipt = %q/%q", tn.Nodes[1].ID, tn.Nodes[1].ToolCallID)
	}
}

// S11 — the purity property. Turns() may not depend on whether the tail is
// open: composing every prefix must agree with composing the whole and
// truncating. This is what stops the screen rewriting itself at seal.
func TestTurns_PureUnderOpenTail(t *testing.T) {
	msgs := []message.Message{
		userPrompt("q1"),
		assistant(message.TextContent("a1")),
		userPrompt("q2"),
		assistant(invoke("t1", "bash", "echo hi")),
		toolResultTic(result("t1", "bash", "hi", false)),
		assistant(message.TextContent("a2")),
	}
	for i := range msgs {
		msgs[i].LogicalTime = uint64(i + 1)
	}
	full := Turns(append([]message.Message(nil), msgs...), nil, nil)

	for n := 1; n <= len(msgs); n++ {
		prefix := Turns(append([]message.Message(nil), msgs[:n]...), nil, nil)
		for i, tn := range prefix {
			if i >= len(full) {
				t.Fatalf("prefix %d produced turn %d beyond the full projection", n, i)
			}
			// Every turn the prefix completed must match the full projection
			// exactly; only the last (still-growing) turn may be shorter.
			if i < len(prefix)-1 && len(tn.Nodes) != len(full[i].Nodes) {
				t.Errorf("prefix %d turn %d: %d nodes, full has %d",
					n, i, len(tn.Nodes), len(full[i].Nodes))
			}
			for j := range tn.Nodes {
				if tn.Nodes[j].ID != full[i].Nodes[j].ID {
					t.Errorf("prefix %d turn %d node %d: id %q != %q (ids must not move)",
						n, i, j, tn.Nodes[j].ID, full[i].Nodes[j].ID)
				}
			}
		}
	}
}

// Invariant 6: an empty block is minted, not skipped, so nodes after it cannot
// shift when it fills. The renderer is what hides empties.
func TestTurns_EmptyBlocksAreMintedNotSkipped(t *testing.T) {
	empty := message.Message{Role: message.RoleOutput, LogicalTime: 2, Content: []message.Content{
		{Type: message.ContentThinking, Text: ""},
		{Type: message.ContentProse, Text: "answer"},
	}}
	filled := empty
	filled.Content = []message.Content{
		{Type: message.ContentThinking, Text: "now I think"},
		{Type: message.ContentProse, Text: "answer"},
	}
	prompt := userPrompt("q")
	prompt.LogicalTime = 1

	before := Turns([]message.Message{prompt, empty}, nil, nil)
	after := Turns([]message.Message{prompt, filled}, nil, nil)

	if len(before[0].Nodes) != len(after[0].Nodes) {
		t.Fatalf("node count moved when the empty block filled: %d -> %d",
			len(before[0].Nodes), len(after[0].Nodes))
	}
	for i := range before[0].Nodes {
		if before[0].Nodes[i].ID != after[0].Nodes[i].ID {
			t.Errorf("node %d id moved: %q -> %q", i, before[0].Nodes[i].ID, after[0].Nodes[i].ID)
		}
	}
}
