package outfit

import "testing"

func TestSkillNameTakesOnlyMarkdown(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"howto.md":                     {"howto", true},
		"claude-design.md":             {"claude-design", true},
		"my.notes.md":                  {"my.notes", true},
		"howto.md.pre-bundle-20260629": {"", false},
		"howto.md.bak":                 {"", false},
		"notes.txt":                    {"", false},
		"README":                       {"", false},
		".hidden.md":                   {"", false},
		".md":                          {"", false},
	}
	for file, want := range cases {
		got, ok := skillName(file)
		if ok != want.ok || got != want.want {
			t.Errorf("%s -> (%q, %v), want (%q, %v)", file, got, ok, want.want, want.ok)
		}
	}
}
