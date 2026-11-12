package cli

import "testing"

// The verb attribute must never carry prompt text: anything that is not a
// command is reported as "prompt".
func TestCLIVerbNeverCarriesPromptText(t *testing.T) {
	known := func(s string) bool { return s == "ls" || s == "state" }
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "help"},
		{[]string{"ls", "--json"}, "ls"},
		{[]string{"state", "show"}, "state"},
		{[]string{"please delete my secrets"}, "prompt"},
		{[]string{"--", "hello"}, "prompt"},
	} {
		if got := cliVerb(c.args, known); got != c.want {
			t.Errorf("%q: got %q, want %q", c.args, got, c.want)
		}
	}
}
