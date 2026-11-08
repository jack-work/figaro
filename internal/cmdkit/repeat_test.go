package cmdkit

import (
	"bytes"
	"strings"
	"testing"
)

func repeatRouter(got **RunContext, errOut *bytes.Buffer) *Router {
	r := NewRouter("figaro")
	r.Stdout, r.Stderr = &bytes.Buffer{}, errOut
	r.Register(&Command{
		Name: "form",
		Flags: []FlagDef{
			{Long: "set", Short: "S", Repeats: true},
			{Long: "delete", Short: "D", Repeats: true},
			{Long: "outfit", Short: "O"},
			{Long: "limit", Short: "n", Default: "10"},
			{Long: "json", Short: "j", IsBool: true},
		},
		Run: func(ctx *RunContext) error { *got = ctx; return nil },
	})
	return r
}

// `-S a=1 -S b=2` kept only b=2: the router held one value per flag and a
// second occurrence overwrote the first without a word. A flag that repeats
// joins its occurrences in order, as one comma list, however each is spelled.
func TestARepeatingFlagJoinsEveryOccurrence(t *testing.T) {
	var got *RunContext
	var errOut bytes.Buffer
	r := repeatRouter(&got, &errOut)
	code := r.Run([]string{"form", "-S", "a=1", "--set", "b=2", "--set=a=3", "-Sc=4", "-D", "x", "--delete=y"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if s := got.Flag("set"); s != "a=1,b=2,a=3,c=4" {
		t.Errorf("--set = %q, want every occurrence in order", s)
	}
	if d := got.Flag("delete"); d != "x,y" {
		t.Errorf("--delete = %q, want x,y", d)
	}
}

// Any other value flag given twice is refused by name, rather than one of
// the two being dropped; a default does not count as given, and a bool flag
// may repeat.
func TestAValueFlagGivenTwiceIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"-O", "a", "-O", "b"},
		{"--outfit", "a", "--outfit=b"},
		{"-n", "1", "--limit", "2"},
	} {
		var got *RunContext
		var errOut bytes.Buffer
		r := repeatRouter(&got, &errOut)
		code := r.Run(append([]string{"form"}, args...))
		if code == 0 || got != nil {
			t.Errorf("%v: ran with %v", args, got.Flags)
			continue
		}
		long := "--outfit"
		if args[0] == "-n" {
			long = "--limit"
		}
		if !strings.Contains(errOut.String(), long) || !strings.Contains(errOut.String(), "twice") {
			t.Errorf("%v: refusal %q does not name %s as given twice", args, errOut.String(), long)
		}
	}
	var got *RunContext
	var errOut bytes.Buffer
	r := repeatRouter(&got, &errOut)
	if code := r.Run([]string{"form", "-n", "3", "-j", "-j"}); code != 0 {
		t.Fatalf("a default overridden once, and a bool given twice, were refused: %s", errOut.String())
	}
	if got.Flag("limit") != "3" {
		t.Errorf("--limit = %q, want 3 over the default", got.Flag("limit"))
	}
}
