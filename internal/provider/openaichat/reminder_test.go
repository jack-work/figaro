package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

// Chalkboard patches must reach the model on this dialect too. The projection
// has always attached them to the message; only the Anthropic encoders drew
// them, so every state change (an outfit fold, a `set`) was invisible to every
// OpenAI-compatible endpoint.
func TestEncodeRendersChalkboardReminders(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(provider.Knobs{Model: "m"}, staticToken("k"),
		provider.UncachedRoute("http://example.invalid/v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Templates = tmpls

	msg := message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{{Type: message.ContentProse, Text: "second"}},
		Patches: []message.Patch{{Set: map[string]json.RawMessage{
			"focus_mode": json.RawMessage(`"deep"`),
		}}},
	}
	encoded, err := p.encode(msg, chalkboard.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	joined := string(join(encoded))
	if !strings.Contains(joined, `system-reminder name=\"focus_mode\"`) {
		t.Fatalf("no reminder in %s", joined)
	}
	if !strings.Contains(joined, "second") {
		t.Fatalf("prompt lost: %s", joined)
	}

	// No templates configured means no reminders, not a crash.
	p.Templates = nil
	encoded, err = p.encode(msg, chalkboard.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(join(encoded)), "system-reminder") {
		t.Fatal("rendered without templates")
	}
}

func join(msgs []json.RawMessage) []byte {
	var out []byte
	for _, m := range msgs {
		out = append(out, m...)
	}
	return out
}
