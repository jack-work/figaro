package cli

import (
	"testing"
)

// TestHasPreDashFlag verifies the scan-until-`--` helper used by
// PassRaw commands to detect flags (like --json/-j on `figaro new`)
// before the `--` boundary that separates flags from the prompt body.
func TestHasPreDashFlag(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		names []string
		want  bool
	}{
		{"empty", nil, []string{"--json"}, false},
		{"present at start", []string{"--json", "--", "hi"}, []string{"--json"}, true},
		{"present short", []string{"-j", "--", "hi"}, []string{"--json", "-j"}, true},
		{"absent", []string{"--", "hi"}, []string{"--json"}, false},
		{"after dashdash ignored", []string{"--", "--json"}, []string{"--json"}, false},
		{"no dashdash", []string{"--json"}, []string{"--json"}, true},
		{"multiple names, first match", []string{"-j"}, []string{"--json", "-j"}, true},
		{"multiple names, second match", []string{"--json"}, []string{"-j", "--json"}, true},
		{"unrelated flags", []string{"--verbose", "-r", "--", "x"}, []string{"--json"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPreDashFlag(tc.args, tc.names...); got != tc.want {
				t.Errorf("hasPreDashFlag(%v, %v) = %v, want %v", tc.args, tc.names, got, tc.want)
			}
		})
	}
}

// TestPreDashFlagValue exercises the string-valued flag scanner:
// `--name value`, `--name=value`, aliases, missing values, and the
// dashdash boundary.
func TestPreDashFlagValue(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		names   []string
		wantVal string
		wantOK  bool
		wantErr bool
	}{
		{"absent", []string{"--", "hi"}, []string{"--loadout"}, "", false, false},
		{"space form", []string{"--loadout", "focus", "--", "hi"}, []string{"--loadout", "-L"}, "focus", true, false},
		{"equals form", []string{"--loadout=focus", "--", "hi"}, []string{"--loadout"}, "focus", true, false},
		{"short alias", []string{"-L", "focus", "--", "hi"}, []string{"--loadout", "-L"}, "focus", true, false},
		{"missing value at end", []string{"--loadout"}, []string{"--loadout"}, "", false, true},
		{"missing value before dashdash", []string{"--loadout", "--", "hi"}, []string{"--loadout"}, "", false, true},
		{"empty after equals", []string{"--loadout=", "--", "hi"}, []string{"--loadout"}, "", false, true},
		{"after dashdash ignored", []string{"--", "--loadout", "focus"}, []string{"--loadout"}, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok, err := preDashFlagValue(tc.args, tc.names...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error; got val=%q ok=%v", v, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v != tc.wantVal || ok != tc.wantOK {
				t.Errorf("preDashFlagValue(%v, %v) = (%q, %v); want (%q, %v)", tc.args, tc.names, v, ok, tc.wantVal, tc.wantOK)
			}
		})
	}
}
