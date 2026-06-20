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
	units := Units(msgs)

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
	})
	if len(units) != 2 || units[0].Role != "user" {
		t.Fatalf("control tic should not produce a unit; got %+v", units)
	}
}
