package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/keyconf"
)

func TestToggleReadsBooleansAndRefusesTheRest(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "null": false, "true": true, "false": false, `"true"`: true, `"FALSE"`: false} {
		got, err := toggleState(json.RawMessage(raw))
		if err != nil || got != want {
			t.Errorf("%s: got %v %v, want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{`"medium"`, `1`, `{"a":1}`} {
		if _, err := toggleState(json.RawMessage(raw)); err == nil {
			t.Errorf("%s toggled; only true and false flip", raw)
		}
	}
}

func TestABareColonBindsNothing(t *testing.T) {
	diags := withUserKeys(t, keyconf.File{Bind: map[string]map[string]string{"user": {"M-e": ": "}}})
	if len(diags) != 1 || !strings.Contains(diags[0].text, "names a command") {
		t.Fatalf("diags = %v", diags)
	}
}
