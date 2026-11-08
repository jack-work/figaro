package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

// `figaro form new -S brief=kept -S nest.a=1` minted a form holding only
// nest.a: the router kept the last -S. Every verb that takes -S and -D now
// sees all of them, in order.
func TestEveryRoutedVerbKeepsEverySetAndDelete(t *testing.T) {
	r := buildRouter("figaro", &config.Loaded{})
	for verb, args := range map[string][]string{
		"form":    {"new", "-S", "brief=kept", "-S", "nest.a=1", "-D", "x", "-D", "y"},
		"bind":    {"@x", "-S", "brief=kept", "--set", "nest.a=1", "-D", "x", "--delete=y"},
		"cast":    {"@x", "-S", "brief=kept", "-S", "nest.a=1", "-D", "x", "-D", "y"},
		"succeed": {"@x", "-S", "brief=kept", "-S", "nest.a=1", "-D", "x", "-D", "y"},
	} {
		ctx, err := r.Parse(verb, args)
		if err != nil {
			t.Errorf("%s: %v", verb, err)
			continue
		}
		if got := ctx.Flag("set"); got != "brief=kept,nest.a=1" {
			t.Errorf("%s: --set = %q, want both", verb, got)
		}
		if got := ctx.Flag("delete"); got != "x,y" {
			t.Errorf("%s: --delete = %q, want both", verb, got)
		}
	}
	if _, err := r.Parse("form", []string{"new", "-O", "a", "-O", "b", "-S", "k=v"}); err == nil {
		t.Errorf("form new took -O twice and kept one")
	}
}

// send and new parse their own flags: --id already refused a second value,
// but --record kept the last one silently, so a tape landed where the user
// had not looked. Both are refused, naming the flag; -O, -S and -D still join.
func TestSendRefusesARepeatedIDOrRecord(t *testing.T) {
	for _, args := range [][]string{
		{"--record", "a.tape", "--record", "b.tape", "--", "hi"},
		{"--record=a.tape", "--record", "b.tape", "--", "hi"},
		{"--id", "abcd1234", "--id=dcba4321", "--", "hi"},
	} {
		_, _, err := extractPromptFlags(args, false)
		if err == nil || !strings.Contains(err.Error(), "given more than once") {
			t.Errorf("%v: %v, want a refusal naming the flag", args, err)
		}
	}
	opts, _, err := extractPromptFlags([]string{"-O", "a", "-O", "b", "-S", "x=1", "-S", "y=2", "--", "hi"}, false)
	if err != nil {
		t.Fatalf("-O and -S repeated: %v", err)
	}
	if opts.setText != "x=1,y=2" {
		t.Errorf("-S joined to %q, want x=1,y=2", opts.setText)
	}
}
