package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// A reminder is never the only text in a user-role message: in a results-only
// round it rides inside the last tool_result, in the same round.
func TestAReminderOnAToolRoundRidesInsideTheToolResult(t *testing.T) {
	p := &Anthropic{Templates: mustTemplates(t)}
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call_1", "bash", `set mantra = "named myself"`, false),
		},
		Patches: []message.Patch{mantraPatch()},
	}
	raws, err := p.encode(msg, form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) != 1 {
		t.Fatalf("encoded to %d messages, want 1", len(raws))
	}
	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raws[0], &wire); err != nil {
		t.Fatal(err)
	}
	var inResult bool
	for _, b := range wire.Content {
		switch b.Type {
		case "text":
			t.Fatalf("a top-level text block in a tool-results-only message: %q\n"+
				"this is the block a model reads as its master saying nothing", b.Text)
		case "tool_result":
			for _, c := range b.Content {
				if strings.Contains(c.Text, `<system-reminder name="mantra">`) {
					inResult = true
				}
			}
		}
	}
	if !inResult {
		t.Fatalf("the reminder was not delivered in this round at all: %s", raws[0])
	}
}

func TestAReminderBesidePersonSpeechStaysAtTheTopLevel(t *testing.T) {
	p := &Anthropic{Templates: mustTemplates(t)}
	msg := message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("what next?")},
		Patches: []message.Patch{mantraPatch()},
	}
	raws, err := p.encode(msg, form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raws[0])
	if !strings.Contains(s, `"type":"text","text":"\u003csystem-reminder name=\"mantra\"`) &&
		!strings.Contains(s, `"text":"\u003csystem-reminder name=\"mantra\"`) {
		t.Fatalf("the reminder should sit at the top level beside the prose: %s", s)
	}
}

func mantraPatch() message.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"named myself"`),
	}, nil)
}

func mustTemplates(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}
