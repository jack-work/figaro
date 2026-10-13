package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func TestEncodeRendersFormReminders(t *testing.T) {
	tmpls, err := form.LoadDefaultTemplates()
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
		Patches: []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"focus_mode": json.RawMessage(`"deep"`),
		}, nil)},
	}
	encoded, err := p.encode(msg, form.Snapshot{})
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

	p.Templates = nil
	encoded, err = p.encode(msg, form.Snapshot{})
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
