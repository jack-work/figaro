package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// A study outlives the form it watched; the figaro must still be able to let
// it go, by the id its study list records.
func TestAStudyOfADeletedFormCanBeDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	aria := rig.mint(t, "observer")
	out, err := rig.cmd("form", "new", "-j", "-S", "brief=gone").Output()
	if err != nil {
		t.Fatalf("form new: %v\n%s", err, out)
	}
	var made struct {
		FormID string `json:"form_id"`
	}
	if err := json.Unmarshal(lastLine(out), &made); err != nil || made.FormID == "" {
		t.Fatalf("form new -j printed no form id: %q", out)
	}
	rig.must(t, "study", aria, made.FormID)
	rig.must(t, "form", "rm", made.FormID)

	short := made.FormID[:9]
	if out, err := rig.cmd("drop", aria, short).CombinedOutput(); err != nil {
		t.Fatalf("drop of a deleted form's study: %v\n%s", err, out)
	}
	out, err = rig.cmd("study", aria, "-j").Output()
	if err != nil {
		t.Fatalf("study -j: %v\n%s", err, out)
	}
	if strings.Contains(string(out), strings.TrimPrefix(short, "@")) {
		t.Fatalf("the deleted form is still studied: %s", out)
	}
}
