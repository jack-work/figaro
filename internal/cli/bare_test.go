package cli

import "testing"

// TestIsBareForm pins the routing decision for `figaro … -- <prompt>`.
//
// The bug this file exists for: the old fast path matched on "is there a
// prompt after `--`" and then threw away every token before it: so
// `figaro --id X -- hi` prompted the pid-bound aria (minting a new one when
// the shell had no binding) and said nothing about it.
func TestIsBareForm(t *testing.T) {
	router := buildRouter("figaro", nil)
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"bare prompt", []string{"--", "hello"}, true},
		{"flags then prompt", []string{"--id", "abc12345", "--", "hello"}, true},
		{"short flags then prompt", []string{"-o", "-l", "--", "hello"}, true},
		{"known command with boundary", []string{"send", "--", "hello"}, false},
		{"known command alias", []string{"qua", "--", "hello"}, false},
		{"new with boundary", []string{"new", "--", "hello"}, false},
		// `plain`, `x` and `exec` were deprecated aliases of send and are
		// gone; they now behave like any other unknown leading word: the
		// bare form claims them and rejects them with a did-you-mean.
		{"removed plain", []string{"plain", "--", "hello"}, true},
		{"removed x", []string{"x", "--", "hello"}, true},
		// `--` stays mandatory: without it nothing is a prompt, so a
		// mistyped subcommand still reaches the router's did-you-mean.
		{"no boundary", []string{"hello", "world"}, false},
		{"help", []string{"help"}, false},
		{"bare help flag", []string{"--help"}, false},
		// A typo'd subcommand WITH a boundary is claimed by the bare form,
		// which rejects a leading bare word (see runBarePrompt): either
		// way it is an error, never a silent prompt.
		{"typo'd command with boundary", []string{"shwo", "--", "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBareForm(tc.args, router.HasCommand); got != tc.want {
				t.Fatalf("isBareForm(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestHasDashBoundary(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--"}, true},
		{[]string{"-o", "--", "x"}, true},
		{[]string{"--id=x"}, false},
		{[]string{"---"}, false},
		{[]string{"--", "a", "--", "b"}, true},
	}
	for _, tc := range cases {
		if got := hasDashBoundary(tc.args); got != tc.want {
			t.Errorf("hasDashBoundary(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// TestBareFormPromptIsUnchanged guards the one thing that must not move:
// a `--` inside the prompt body is prompt text. extractPrompt splits on
// the FIRST boundary and joins the rest with spaces.
func TestBareFormPromptIsUnchanged(t *testing.T) {
	opts, rest, err := extractSendFlags([]string{"--id", "abc12345", "--", "text", "containing", "--", "two", "dashes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.id != "abc12345" {
		t.Fatalf("id: got %q, want abc12345", opts.id)
	}
	if got, want := extractPrompt(rest), "text containing -- two dashes"; got != want {
		t.Fatalf("prompt: got %q, want %q", got, want)
	}
}
