package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

func userPrompt(text string) message.Message {
	return message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}}
}

// unitText flattens a unit's nodes to one string for assertions.
func unitText(u Unit) string {
	var b strings.Builder
	for _, n := range u.Nodes {
		if n.Type == livedoc.NodeTool {
			b.WriteString(n.Name + " " + n.Output + "\n")
		} else {
			b.WriteString(n.Markdown + "\n")
		}
	}
	return b.String()
}

func TestUnits_SegmentsByPrompt(t *testing.T) {
	msgs := []message.Message{
		userPrompt("first question"),
		assistant(message.TextContent("first answer")),
		userPrompt("second question"),
		assistant(invoke("t1", "bash", "echo hi")),
		toolResultTic(result("t1", "bash", "hi", false)),
		assistant(message.TextContent("second answer")),
	}
	units := Units(msgs, nil)

	want := []struct {
		role     string
		contains string
	}{
		{"user", "first question"},
		{"assistant", "first answer"},
		{"user", "second question"},
		{"assistant", "second answer"},
	}
	if len(units) != len(want) {
		t.Fatalf("got %d units, want %d: %+v", len(units), len(want), units)
	}
	for i, w := range want {
		if units[i].Role != w.role {
			t.Errorf("unit %d: role %q want %q", i, units[i].Role, w.role)
		}
		if !strings.Contains(unitText(units[i]), w.contains) {
			t.Errorf("unit %d: %q missing %q", i, unitText(units[i]), w.contains)
		}
	}
	// The second assistant turn folds the tool invoke + result together:
	// a prose node plus a tool node carrying its output.
	if got := unitText(units[3]); !strings.Contains(got, "bash") || !strings.Contains(got, "hi") {
		t.Errorf("assistant turn should carry the folded tool output:\n%s", got)
	}
}

func TestUnits_SkipsControlOnlyTics(t *testing.T) {
	// A control-only user tic (patches, no text) is not a prompt unit.
	control := message.Message{Role: message.RoleUser}
	units := Units([]message.Message{
		control,
		userPrompt("hello"),
		assistant(message.TextContent("hi there")),
	}, nil)
	if len(units) != 2 || units[0].Role != "user" {
		t.Fatalf("control tic should not produce a unit; got %+v", units)
	}
}

func TestUnits_SteeringFoldsIntoTurn(t *testing.T) {
	msgs := []message.Message{
		userPrompt("hello"),
		{Role: message.RoleAssistant, Content: []message.Content{
			{Type: message.ContentProse, Text: "hey"},
			{Type: message.ContentToolInvoke, ToolCallID: "t1", ToolName: "test",
				Arguments: map[string]interface{}{"x": 1}},
		}},
		{Role: message.RoleUser, Content: []message.Content{
			message.ToolResultContent("t1", "test", "result-out", false),
			{Type: message.ContentProse, Text: "oh and by the way"}, // the steer
		}},
		{Role: message.RoleAssistant, Content: []message.Content{
			{Type: message.ContentProse, Text: "oh cool sure"},
		}},
	}
	units := Units(msgs, nil)
	// Two units: the user prompt, then ONE assistant turn (the steer does NOT
	// start a third unit).
	if len(units) != 2 {
		t.Fatalf("want 2 units (prompt + turn), got %d: %+v", len(units), units)
	}
	if units[0].Role != "user" || strings.TrimSpace(unitText(units[0])) != "hello" {
		t.Fatalf("unit 0 = %+v", units[0])
	}
	a := units[1]
	if a.Role != "assistant" {
		t.Fatalf("unit 1 role = %q", a.Role)
	}
	var got []livedoc.NodeType
	for _, n := range a.Nodes {
		got = append(got, n.Type)
	}
	want := []livedoc.NodeType{livedoc.NodeProse, livedoc.NodeTool, livedoc.NodeSteering, livedoc.NodeProse}
	if len(got) != len(want) {
		t.Fatalf("node types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("node[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// the steering node carries the steer text; the tool node carries the result
	if a.Nodes[2].Markdown != "oh and by the way" {
		t.Fatalf("steering node text = %q", a.Nodes[2].Markdown)
	}
	if !strings.Contains(a.Nodes[1].Output, "result-out") {
		t.Fatalf("tool node should fold its result: %q", a.Nodes[1].Output)
	}
}
