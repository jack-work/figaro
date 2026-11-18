package authz

import "testing"

// Defends give-only-what-you-hold on key globs: a held glob covers a granted
// one only when every key the granted glob reaches is reached by the held one.
// The first six rows are the P2+P5 review's (F4); matching the granted glob
// as if it were a key name passed the first four.
func TestAHeldGlobCoversAGrantedOneStructurally(t *testing.T) {
	for _, c := range []struct {
		held, granted string
		want          bool
	}{
		{"notes.*", "notes.**", false},
		{"@self", "**", false},
		{"@self", "system.**", false},
		{"*", "**", false},
		{"notes.*", "notes.*.*", false},
		{"notes.a", "notes.a.b", false},
		{"@self", "system.model", false},
		{"@self", "@lineage", false},
		{"notes.*", "@self", false},
		{"system.thinking_*", "system.thinking_*", true},
		{"system.thinking_*", "system.thinking_budget", true},
		{"system.thinking_*", "system.*", false},
		{"@self", "mantra", true},
		{"@self", "@self", true},
		{"**", "@self", true},
		{"**", "notes.**", true},
		{"notes.**", "notes.a.**", true},
		{"notes.**", "notes.*.b", true},
		{"notes.*", "notes.a", true},
		{"", "notes.**", true},
		{"notes.**", "", false},
	} {
		if got := globCovers(c.held, c.granted); got != c.want {
			t.Errorf("globCovers(%q, %q) = %v, want %v", c.held, c.granted, got, c.want)
		}
	}
}
