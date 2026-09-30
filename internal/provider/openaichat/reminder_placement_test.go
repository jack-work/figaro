package openaichat

import (
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// A tool round with nobody speaking must not grow a `user` message holding
// only a reminder: on this dialect that is, byte for byte, the user sending an
// empty prompt. The reminder rides inside the last tool message instead, in
// the same round.
func TestAReminderOnAToolRoundRidesInsideTheToolMessage(t *testing.T) {
	p := &Provider{Templates: mustTemplates(t)}
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call_1", "bash", `set mantra = "named myself"`, false),
		},
		Patches: []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"mantra": json.RawMessage(`"named myself"`),
		}, nil)},
	}
	raws, err := p.encode(msg, form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	delivered := false
	for _, raw := range raws {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, m.Role)
		if m.Role == "user" {
			t.Fatalf("a user message appeared in a tool round nobody spoke in (roles %v): %s", roles, m.Content)
		}
		if m.Role == "tool" && strings.Contains(string(m.Content), "system-reminder") {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("the reminder was not delivered in this round (roles %v)", roles)
	}
}

func mustTemplates(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}
