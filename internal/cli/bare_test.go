package cli

import "testing"

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
		{"removed plain", []string{"plain", "--", "hello"}, true},
		{"removed x", []string{"x", "--", "hello"}, true},
		{"no boundary", []string{"hello", "world"}, false},
		{"help", []string{"help"}, false},
		{"bare help flag", []string{"--help"}, false},
		// Claimed by the bare form, which then rejects the leading word:
		// never a silent prompt.
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

// A `--` inside the prompt body is prompt text: only the first boundary splits.
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
