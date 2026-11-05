package rpc

import "testing"

// Row patterns name branches by their labels, never a dotted path: a model id
// like x-ai/grok-4.1 is one segment. * is one segment, ** one or more, and the
// group is the segment the first * took when a later * takes the row itself;
// a pattern with one * has no group.
func TestMatchPath(t *testing.T) {
	cases := []struct {
		patterns []string
		segs     []string
		group    string
		ok       bool
	}{
		{[]string{"items.*"}, []string{"items", "ab12"}, "", true},
		{[]string{"items.*"}, []string{"items", "ab12", "mantra"}, "", false},
		{[]string{"items.*"}, []string{"items"}, "", false},
		{[]string{"*.models.*"}, []string{"openrouter", "models", "x-ai/grok-4.1"}, "openrouter", true},
		{[]string{"*.models.*"}, []string{"openrouter", "models"}, "", false},
		{[]string{"**"}, []string{"a"}, "", true},
		{[]string{"**"}, []string{"a", "b", "c"}, "", true},
		{[]string{"**.forks.*"}, []string{"a", "forks", "b"}, "", true},
		{[]string{"x", "items.*"}, []string{"items", "q"}, "", true},
	}
	for _, c := range cases {
		g, ok := MatchPath(c.patterns, c.segs)
		if ok != c.ok || g != c.group {
			t.Errorf("MatchPath(%v, %q) = %q, %v; want %q, %v", c.patterns, c.segs, g, ok, c.group, c.ok)
		}
	}
}
