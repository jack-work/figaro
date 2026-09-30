package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// A REMINDER IS NEVER THE ONLY THING SAID IN A USER-ROLE MESSAGE.
//
// A tool round returns in a user-role message because that is the only role
// a tool_result may ride. When the aria sets its own mantra as the last act
// of an answer, the patch lands on that tool round, and before this the
// reminder went out as a top-level text block beside the tool_result: the
// only TEXT in the message. Models read that as their master speaking and
// saying nothing. Reproduced live on claude-opus-5 by replaying turn 24 of a
// real aria (1ce1f09e) from its real context; the model's own thinking:
//
//	"This message appears to be empty aside from the system reminder--there's
//	 no actual user content to act on."
//
// and to the user: "No question came through -- empty message."
//
// The reminder must still arrive in the SAME round (a patch is seen when it is
// ready), so it moves inside the tool_result rather than being held back.
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

// When a person DID speak, the reminder stays beside their words, as a
// top-level block, exactly as before. Only the message with no speaker moves.
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

// mustTemplates is the renderer production uses. Without it FoldRender emits
// nothing, and a test of WHERE a reminder lands proves only that none was
// built.
func mustTemplates(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}
