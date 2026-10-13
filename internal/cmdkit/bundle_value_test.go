package cmdkit

import (
	"strings"
	"testing"
)

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
