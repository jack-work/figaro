package copilot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// A tool round with nobody speaking must not grow a user message holding only
// a reminder: the model reads that as its master sending an empty prompt. The
// reminder rides inside the last function_call_output instead, in the same
// round.
func TestAReminderOnAToolRoundRidesInsideTheFunctionOutput(t *testing.T) {
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call_1", "bash", `set mantra = "named myself"`, false),
		},
	}
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"named myself"`),
	}, nil)
	items, err := encodeResponseMessage(msg, []message.Patch{patch}, form.Snapshot{}, tpl)
	if err != nil {
		t.Fatal(err)
	}
	delivered := false
	for _, raw := range items {
		var it struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			Output string `json:"output"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			t.Fatal(err)
		}
		if it.Role == "user" {
			t.Fatalf("a user message appeared in a tool round nobody spoke in: %s", raw)
		}
		if it.Type == "function_call_output" && strings.Contains(it.Output, "system-reminder") {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("the reminder was not delivered in this round: %s", items)
	}
}
