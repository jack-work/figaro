package cmdkit

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout swaps os.Stdout for the duration of fn and returns
// what was written. Needed because runComplete prints with fmt.Println.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wPipe
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(rPipe)
		done <- b
	}()
	fn()
	wPipe.Close()
	os.Stdout = old
	return string(<-done)
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

	out := captureStdout(t, func() {
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

	out := captureStdout(t, func() {
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

	out := captureStdout(t, func() {
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

	captureStdout(t, func() {
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
	captureStdout(t, func() {
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
	captureStdout(t, func() {
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
	captureStdout(t, func() {
		r.Run([]string{"__complete", "send", "--", "--id", "myid"})
	})
	if sawPast {
		t.Errorf("PastSeparator true with no user --; args=%v", sawArgs)
	}

	// With a user "--" in the tail: PastSeparator must be true and
	// the "--" must be preserved in Args so downstream logic can
	// locate it.
	captureStdout(t, func() {
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
	captureStdout(t, func() {
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

	out := captureStdout(t, func() {
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
	r.Register(&Command{Name: "set", Short: "Patch a chalkboard key"})

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
// Both generated scripts used to test position alone — fish `$tokens[2] = "--"`
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
			[]string{`for w in "${COMP_WORDS[@]:1}"`, `case " $commands " in *" $verb "*)`}},
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
