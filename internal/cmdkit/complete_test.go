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
