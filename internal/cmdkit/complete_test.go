package cmdkit

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// captureStdout swaps os.Stdout for the duration of fn and returns
// what was written. Needed because runComplete prints with fmt.Println.
// captureRouter runs fn with the router's Stdout on a buffer and returns what
// it wrote. It used to swap os.Stdout, which only worked while __complete
// printed round the router; see TestCompleteWritesToTheRoutersStdout.
func captureRouter(t *testing.T, r *Router, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := r.Stdout
	r.Stdout = &buf
	defer func() { r.Stdout = prev }()
	fn()
	return buf.String()
}

func TestCompleteDispatch(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name: "set",
		Run:  func(*RunContext) error { return nil },
		CompleteArgs: func(ctx *CompleteContext) []string {
			return []string{"alpha", "beta", "gamma"}
		},
	})

	out := captureRouter(t, r, func() {
		code := r.Run([]string{"__complete", "set"})
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	got := strings.Fields(out)
	if len(got) != 3 || got[0] != "alpha" || got[2] != "gamma" {
		t.Errorf("candidates = %v", got)
	}
}

func TestCompleteUnknownVerbSilent(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}

	out := captureRouter(t, r, func() {
		code := r.Run([]string{"__complete", "nope"})
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected silent output, got %q", out)
	}
}

func TestCompleteNoCallbackSilent(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name: "bare",
		Run:  func(*RunContext) error { return nil },
	})

	out := captureRouter(t, r, func() {
		code := r.Run([]string{"__complete", "bare"})
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected silent output, got %q", out)
	}
}

func TestCompleteContextArgs(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	var captured []string
	r.Register(&Command{
		Name: "set",
		Run:  func(*RunContext) error { return nil },
		CompleteArgs: func(ctx *CompleteContext) []string {
			captured = ctx.Args
			return nil
		},
	})

	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "set", "--", "system.tags", "extra"})
	})
	if len(captured) != 2 || captured[0] != "system.tags" || captured[1] != "extra" {
		t.Errorf("Args = %v", captured)
	}
}

func TestCompleteContextCurrent(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	var sawCurrent string
	var sawArgs []string
	r.Register(&Command{
		Name: "send",
		Run:  func(*RunContext) error { return nil },
		CompleteArgs: func(ctx *CompleteContext) []string {
			sawCurrent = ctx.Current
			sawArgs = ctx.Args
			return nil
		},
	})

	// With --current present: dispatcher must pop it off and surface
	// it in Current; Args must not contain it.
	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "send", "--current", "@mod", "--", "hello"})
	})
	if sawCurrent != "@mod" {
		t.Errorf("Current = %q, want %q", sawCurrent, "@mod")
	}
	if len(sawArgs) != 1 || sawArgs[0] != "hello" {
		t.Errorf("Args = %v, want [hello]", sawArgs)
	}

	// Without --current: backward-compatible; Current is empty.
	sawCurrent = "sentinel"
	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "send", "--", "hello"})
	})
	if sawCurrent != "" {
		t.Errorf("Current = %q, want empty when --current omitted", sawCurrent)
	}
}

func TestCompleteContextPastSeparator(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	var sawPast bool
	var sawArgs []string
	r.Register(&Command{
		Name: "send",
		Run:  func(*RunContext) error { return nil },
		CompleteArgs: func(ctx *CompleteContext) []string {
			sawPast = ctx.PastSeparator
			sawArgs = ctx.Args
			return nil
		},
	})

	// Without a user "--": PastSeparator must be false. The leading
	// "--" here is the dispatcher's own boundary marker and must NOT
	// count as a user separator.
	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "send", "--", "--id", "myid"})
	})
	if sawPast {
		t.Errorf("PastSeparator true with no user --; args=%v", sawArgs)
	}

	// With a user "--" in the tail: PastSeparator must be true and
	// the "--" must be preserved in Args so downstream logic can
	// locate it.
	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "send", "--", "--id", "myid", "--", "hello"})
	})
	if !sawPast {
		t.Errorf("PastSeparator false with user --; args=%v", sawArgs)
	}
	if len(sawArgs) != 4 || sawArgs[2] != "--" {
		t.Errorf("Args = %v (expected the user -- preserved)", sawArgs)
	}

	// Regression: a user "--" immediately following the dispatcher's
	// own boundary marker (i.e. `figaro send -- <cursor>`, which the
	// shell turns into `__complete send -- --`) must NOT be eaten by
	// the leading-strip. The dispatcher inserts exactly one boundary
	// "--"; any additional ones are user-typed.
	sawPast = false
	sawArgs = nil
	captureRouter(t, r, func() {
		r.Run([]string{"__complete", "send", "--", "--"})
	})
	if !sawPast {
		t.Errorf("PastSeparator false for `verb -- <cursor>`; args=%v", sawArgs)
	}
	if len(sawArgs) != 1 || sawArgs[0] != "--" {
		t.Errorf("Args = %v (expected single user --)", sawArgs)
	}
}

func TestCompleteBarePromptSentinel(t *testing.T) {
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	var sawPast bool
	var called bool
	r.SetBarePromptComplete(func(ctx *CompleteContext) []string {
		called = true
		sawPast = ctx.PastSeparator
		return []string{"prompt-candidate"}
	})

	out := captureRouter(t, r, func() {
		// Shell-side substitution: the user typed `figaro -- <cursor>`
		// (or an alias of it), the script swaps the verb position from
		// "--" to the sentinel before calling __complete.
		r.Run([]string{"__complete", "__bare_prompt"})
	})
	if !called {
		t.Fatalf("bare-prompt callback not invoked")
	}
	if !sawPast {
		t.Errorf("PastSeparator must be true in bare-prompt path")
	}
	if strings.TrimSpace(out) != "prompt-candidate" {
		t.Errorf("output = %q", out)
	}
}

func TestCompletionScriptsMentionDispatcher(t *testing.T) {
	r := NewRouter("figaro")
	r.Register(&Command{Name: "set", Short: "Patch a form key"})

	for _, shell := range []CompletionShell{ShellBash, ShellZsh, ShellFish} {
		t.Run(string(shell), func(t *testing.T) {
			var buf bytes.Buffer
			if err := r.WriteCompletion(&buf, shell); err != nil {
				t.Fatalf("WriteCompletion: %v", err)
			}
			body := buf.String()
			if !strings.Contains(body, "__complete") {
				t.Errorf("%s script missing __complete dispatch:\n%s", shell, body)
			}
			if strings.Contains(body, "__complete") {
				// Hidden command must not appear as a user-visible
				// suggestion at the top level.
				lines := strings.Split(body, "\n")
				for _, line := range lines {
					if strings.Contains(line, "__fish_use_subcommand") &&
						strings.Contains(line, "__complete") {
						t.Errorf("__complete leaked as user-visible candidate: %q", line)
					}
				}
			}
		})
	}
}

// TestBarePromptDetectorSurvivesAMovedBoundary pins the shell-side rule against
// cli.isBareForm: a "--" boundary ANYWHERE after the program name, with a
// non-command in the verb slot.
//
// Both generated scripts used to test position alone: fish `$tokens[2] = "--"`
// and bash `COMP_WORDS[1] = "--"`. That was correct while the bare prompt form
// took no flags. Once `figaro --id A -- <prompt>` became legal the boundary
// moved to word 3, the detector stopped firing, and completion offered the VERB
// list in the middle of a prompt.
func TestBarePromptDetectorSurvivesAMovedBoundary(t *testing.T) {
	r := NewRouter("figaro")
	r.Register(&Command{Name: "send", Short: "s", Run: func(*RunContext) error { return nil }})

	for _, tc := range []struct {
		shell string
		gen   func(io.Writer) error
		// stale is the position-only test that must NOT survive.
		stale string
		// want are fragments proving the boundary is SCANNED for, and that a
		// real command in the verb slot still wins.
		want []string
	}{
		{"fish", r.writeFishCompletion, `test $tokens[2] = "--"`,
			// The verb list is not asserted verbatim: it is every registered
			// command, and cmdkit now registers a built-in `help`, so pinning
			// the exact string would make this test a census of the command
			// table rather than a check of the boundary detector.
			[]string{`contains -- "--" $tokens[2..-1]`, `contains -- $tokens[2] `, `send`}},
		{"bash", r.writeBashCompletion, `if [ "$verb" = "--" ]; then`,
			[]string{`for w in "${toks[@]:1}"`, `case " $commands " in *" $verb "*)`}},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			var b strings.Builder
			if err := tc.gen(&b); err != nil {
				t.Fatal(err)
			}
			got := b.String()
			if strings.Contains(got, tc.stale) {
				t.Errorf("%s still tests the boundary by POSITION (%q); a bare form with flags "+
					"puts `--` past word 2 and the detector silently stops firing", tc.shell, tc.stale)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%s completion missing %q\n--- generated ---\n%s", tc.shell, w, got)
				}
			}
		})
	}
}

// __complete answers on the ROUTER's writer. The pager runs this router
// in-process with Stdout on a buffer; printing to the process's stdout instead
// painted every candidate over the terminal and left the menu empty.
func TestCompleteWritesToTheRoutersStdout(t *testing.T) {
	var buf bytes.Buffer
	r := NewRouter("prog")
	r.Stdout = &buf
	r.Register(&Command{
		Name:         "pick",
		CompleteArgs: func(*CompleteContext) []string { return []string{"alpha", "beta"} },
		Run:          func(*RunContext) error { return nil },
	})
	r.Run([]string{"__complete", "pick", "--current", "", "--"})
	if got := buf.String(); got != "alpha\nbeta\n" {
		t.Fatalf("captured %q; the candidates went somewhere else", got)
	}
}

func describedRouter() *Router {
	r := NewRouter("prog")
	r.Register(&Command{
		Name: "pick",
		Flags: []FlagDef{
			{Long: "id", Short: "i", Description: "target aria"},
			{Long: "json", Short: "j", Description: "machine\toutput", IsBool: true},
		},
		CompleteArgs: func(*CompleteContext) []string {
			return []string{Candidate("alpha", "the first"), "beta", Candidate("alpha", "a duplicate"), "bad\nvalue"}
		},
		Run: func(*RunContext) error { return nil },
	})
	return r
}

// Descriptions are for a caller that asks. Bash feeds the lines to compgen
// -W, which would offer every word of a description as a completion.
func TestCompleteStripsDescriptionsUnlessAsked(t *testing.T) {
	r := describedRouter()
	plain := captureRouter(t, r, func() { r.Run([]string{"__complete", "pick", "--current", "", "--"}) })
	if plain != "alpha\nbeta\n" {
		t.Fatalf("plain = %q; want bare values, deduplicated, without the multi-line one", plain)
	}
	described := captureRouter(t, r, func() {
		r.Run([]string{"__complete", "pick", "--describe", "--current", "", "--"})
	})
	if described != "alpha\tthe first\nbeta\n" {
		t.Fatalf("described = %q", described)
	}
}

// A word that begins with a dash is a flag, for every command, from the flags
// the command already declares.
func TestCompleteOffersFlagsForADash(t *testing.T) {
	r := describedRouter()
	long := captureRouter(t, r, func() {
		r.Run([]string{"__complete", "pick", "--describe", "--current", "--j", "--"})
	})
	if !strings.Contains(long, "--json\tmachine output\n") || !strings.Contains(long, "--id\ttarget aria\n") {
		t.Fatalf("long flags = %q (a tab inside a description must not split it)", long)
	}
	if strings.Contains(long, "\n-j") {
		t.Fatalf("short forms offered for a long-flag word: %q", long)
	}
	short := captureRouter(t, r, func() { r.Run([]string{"__complete", "pick", "--current", "-", "--"}) })
	if !strings.Contains(short, "-j\n") || !strings.Contains(short, "--json\n") {
		t.Fatalf("a bare dash should offer both forms: %q", short)
	}
	past := captureRouter(t, r, func() { r.Run([]string{"__complete", "pick", "--current", "-", "--", "--"}) })
	if strings.Contains(past, "--json") {
		t.Fatalf("past a user-typed --, a dash is prompt text, not a flag: %q", past)
	}
}

// `--id=<TAB>` completes the value of --id, keeping the flag on the front.
func TestCompleteInlineFlagValue(t *testing.T) {
	r := NewRouter("prog")
	r.Register(&Command{
		Name:  "pick",
		Flags: []FlagDef{{Long: "id"}},
		CompleteArgs: func(c *CompleteContext) []string {
			if n := len(c.Args); n > 0 && c.Args[n-1] == "--id" {
				return []string{Candidate("abc123", "an aria")}
			}
			return []string{"positional"}
		},
		Run: func(*RunContext) error { return nil },
	})
	got := captureRouter(t, r, func() { r.Run([]string{"__complete", "pick", "--describe", "--current", "--id=a", "--"}) })
	if got != "--id=abc123\tan aria\n" {
		t.Fatalf("got %q", got)
	}
}

// A PassRaw command parses its own flags and documents them in its own help:
// CompleteFlags are offered by completion and never printed by help.
func TestCompleteFlagsAreForCompletionOnly(t *testing.T) {
	r := NewRouter("prog")
	r.Register(&Command{
		Name:          "say",
		PassRaw:       true,
		Long:          "hand-written help",
		CompleteFlags: []FlagDef{{Long: "raw", Short: "r", Description: "plain"}},
		Run:           func(*RunContext) error { return nil },
	})
	got := captureRouter(t, r, func() { r.Run([]string{"__complete", "say", "--current", "--r", "--"}) })
	if got != "--raw\n" {
		t.Fatalf("completion = %q", got)
	}
	var errb bytes.Buffer
	r.Stderr = &errb
	help := captureRouter(t, r, func() { r.Run([]string{"help", "say"}) }) + errb.String()
	if !strings.Contains(help, "hand-written help") {
		t.Fatalf("the help under test was never captured: %q", help)
	}
	if strings.Contains(help, "--raw") {
		t.Fatalf("help printed a completion-only flag:\n%s", help)
	}
}
