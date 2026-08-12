package cli

import "testing"

// The `-C` grammar, at the parser boundary. These are the shapes Gluck wrote
// down, and the point of testing them here is that each one has to survive
// bundle expansion, which is where the last flag to be added broke.
func TestCastFlagBundlesWithDressing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		outfit string
		set    string
	}{
		{"-CO gangs", []string{"-CO", "pr-reviewer"}, "pr-reviewer", ""},
		{"-CS gangs", []string{"-CS", `name="test"`}, "", `name="test"`},
		{"-CS with a JSON literal", []string{"-CS", `{"name":"test"}`}, "", `{"name":"test"}`},
		{"separate flags", []string{"-C", "-O", "pr-reviewer"}, "pr-reviewer", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := extractSendFlags(tc.args)
			if err != nil {
				t.Fatalf("rejected: %s", err)
			}
			if !opts.cast {
				t.Fatal("-C did not land")
			}
			if opts.outfitText != tc.outfit {
				t.Fatalf("outfit: want %q, got %q", tc.outfit, opts.outfitText)
			}
			if opts.setText != tc.set {
				t.Fatalf("set: want %q, got %q", tc.set, opts.setText)
			}
		})
	}
}

// The role id is the one @-sigiled positional. The sigil is what makes this
// lexical rather than a guess, exactly as it is in the study/cast grammar.
func TestLiftRoleArg(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		bad  bool
	}{
		{"named role", []string{"@abc123"}, "@abc123", false},
		{"no role", []string{}, "", false},
		{"role before a prompt", []string{"@abc123", "--", "do", "the", "thing"}, "@abc123", false},
		{"an @ in the prompt is not a role", []string{"--", "mail", "@someone"}, "", false},
		{"two roles", []string{"@a", "@b"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := liftRoleArg(tc.args)
			if tc.bad {
				if err == nil {
					t.Fatalf("want a grammar error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// The lift must leave the rest of the argv exactly as the prompt parser
// expects it, including the boundary and everything past it.
func TestLiftRoleArgKeepsTheRest(t *testing.T) {
	role, kept, err := liftRoleArg([]string{"-C", "@abc", "-O", "sonn5", "--", "do", "@it"})
	if err != nil {
		t.Fatal(err)
	}
	if role != "@abc" {
		t.Fatalf("role: want @abc, got %q", role)
	}
	want := []string{"-C", "-O", "sonn5", "--", "do", "@it"}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Fatalf("kept %v, want %v", kept, want)
		}
	}
}
