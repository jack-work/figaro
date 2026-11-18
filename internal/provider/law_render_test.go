package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// Defends a studied law against the reader's delta limits: a role's law is
// neither cut nor elided, however small the holder sets them.
func TestAStudiedLawIsNeitherCutNorElided(t *testing.T) {
	law := strings.Repeat("Report to the role. ", 40)
	set := map[string]json.RawMessage{"law.report": mustJSON(law)}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		set[k] = mustJSON(strings.Repeat(k, 200))
	}
	msg := message.Message{
		Role:         message.RoleInput,
		StudyPatches: map[string][]message.Patch{"@role": {form.Build(form.Snapshot{}, set, nil)}},
		StudyAt:      map[string]uint64{"@role": 4},
	}
	body := blockJSON(t, StudyReminderTexts(msg, boundFormWithLimits(64, 300))[0])
	shown, _ := body["set"].(map[string]any)
	if got, _ := shown["law.report"].(string); got != law {
		t.Fatalf("the law was cut or elided: %q", got)
	}
	if len(shown) == 6 {
		t.Fatalf("the limits stopped binding ordinary keys: %v", shown)
	}
	if _, ok := shown["a"]; !ok {
		t.Fatalf("the law's bytes were charged to the reader's budget: %v", shown)
	}
}
