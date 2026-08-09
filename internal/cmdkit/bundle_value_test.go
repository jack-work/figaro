package cmdkit

import (
	"strings"
	"testing"
)

// A short that takes a value ends the bundle and swallows the rest of the
// token as its value; bools before it still expand. Unknown letters leave the
// token untouched so the parser can name them.
func TestExpandBundledValueShorts(t *testing.T) {
	flags := []FlagDef{
		{Long: "ephemeral", Short: "e", IsBool: true},
		{Long: "raw", Short: "r", IsBool: true},
		{Long: "outfit", Short: "O"},
	}
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"-er", "-e -r"},
		{"-eOsonn5", "-e -O sonn5"},
		{"-eO", "-e -O"},
		{"-Osonn5", "-O sonn5"},
		{"-Oa,b=c", "-O a,b=c"},
		{"-erz", "-erz"},
		{"-e", "-e"},
	} {
		got := strings.Join(ExpandBundled([]string{tc.in}, flags), " ")
		if got != tc.want {
			t.Errorf("ExpandBundled(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
