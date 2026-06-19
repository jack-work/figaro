package compose

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func userPrompt(text string) message.Message {
	return message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}}
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
		if !strings.Contains(units[i].Markdown, w.contains) {
			t.Errorf("unit %d: %q missing %q", i, units[i].Markdown, w.contains)
		}
	}
	// The second assistant turn folds the tool invoke + result together.
	if !strings.Contains(units[3].Markdown, "## bash") || !strings.Contains(units[3].Markdown, "hi") {
		t.Errorf("assistant turn should carry the folded tool output:\n%s", units[3].Markdown)
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
