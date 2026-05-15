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
