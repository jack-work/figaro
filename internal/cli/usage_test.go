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
