package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// status must answer for every species, and say which one it is answering
// for. It used to answer only for figaros: `fig status` while attended to a
// form died with `no aria "@58a57f36"` about a form that was sitting right
// there, because the lookup used the working listing (figaros only).
func TestStatusNamesTheSpecies(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  rpc.FigaroInfoResponse
		want string
	}{
		{"a figaro", rpc.FigaroInfoResponse{ID: "abc12345", Kind: "conversation"}, "figaro"},
		{"a plain form", rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form"}, "form"},
		{"a role", rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form", TargetAria: "abc12345"}, "role"},
		{"a legacy stump", rpc.FigaroInfoResponse{ID: "sonn5@deadbeef", Kind: "outfit"}, "form (legacy outfit stump)"},
		// A row from a daemon that does not carry kinds still reads by sigil.
		{"kindless, by sigil", rpc.FigaroInfoResponse{ID: "@a1b2c3d4"}, "form"},
	} {
		if got := speciesOf(&tc.row); got != tc.want {
			t.Errorf("%s: species = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A role is a duck type, so the target may be read from the listing or from
// the board. The board wins when the listing has not caught up with a cast.
func TestRoleTargetFallsBackToTheBoard(t *testing.T) {
	snap := form.Snapshot{}.Apply(form.Patch{Set: map[string]json.RawMessage{
		"target-aria": json.RawMessage(`"fromboard"`),
	}})
	row := rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form"}
	if got := roleTargetOf(&row, snap); got != "fromboard" {
		t.Errorf("board fallback: %q", got)
	}
	row.TargetAria = "fromlisting"
	if got := roleTargetOf(&row, snap); got != "fromlisting" {
		t.Errorf("listing wins when present: %q", got)
	}
	// And a form that is not cast has no target at all, which is the whole
	// difference between a form and a role.
	if got := roleTargetOf(&rpc.FigaroInfoResponse{ID: "@x", Kind: "form"}, form.Snapshot{}); got != "" {
		t.Errorf("uncast form reported a target: %q", got)
	}
}

// The JSON shape carries the species too, so a script does not have to infer
// it from a sigil.
func TestFormStatusJSONCarriesSpecies(t *testing.T) {
	snap := form.Snapshot{}.Apply(form.Patch{Set: map[string]json.RawMessage{
		"name":        json.RawMessage(`"warden"`),
		"target-aria": json.RawMessage(`"abc12345"`),
	}})
	row := rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form", Name: "warden", TargetAria: "abc12345"}
	b, err := json.Marshal(formStatus(&row, snap, 7))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"species":"role"`, `"target_aria":"abc12345"`, `"version":7`, `"keys":2`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}
