package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestStatusNamesTheSpecies(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  rpc.FigaroInfoResponse
		want string
	}{
		{"a figaro", rpc.FigaroInfoResponse{ID: "abc12345", Kind: "conversation"}, "figaro"},
		{"a plain form", rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form"}, "form"},
		{"a role", rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form", TargetAria: "abc12345"}, "role"},
		{"a legacy outfit version", rpc.FigaroInfoResponse{ID: "sonn5@deadbeef", Kind: "outfit"}, "form (legacy outfit version)"},
		{"kindless, by sigil", rpc.FigaroInfoResponse{ID: "@a1b2c3d4"}, "form"},
	} {
		if got := speciesOf(&tc.row); got != tc.want {
			t.Errorf("%s: species = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRoleTargetFallsBackToTheForm(t *testing.T) {
	snap := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"target-aria": json.RawMessage(`"fromform"`),
	}, nil))
	row := rpc.FigaroInfoResponse{ID: "@a1b2c3d4", Kind: "form"}
	if got := roleTargetOf(&row, snap); got != "fromform" {
		t.Errorf("form fallback: %q", got)
	}
	row.TargetAria = "fromlisting"
	if got := roleTargetOf(&row, snap); got != "fromlisting" {
		t.Errorf("listing wins when present: %q", got)
	}
	if got := roleTargetOf(&rpc.FigaroInfoResponse{ID: "@x", Kind: "form"}, form.Snapshot{}); got != "" {
		t.Errorf("uncast form reported a target: %q", got)
	}
}

func TestFormStatusJSONCarriesSpecies(t *testing.T) {
	snap := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"name":        json.RawMessage(`"warden"`),
		"target-aria": json.RawMessage(`"abc12345"`),
	}, nil))
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
