package cmdkit

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpVerb pins `help [<command>]`.
//
// WHAT WAS WRONG. No help verb existed and the router had no fallback, so
// `figaro help` took the unknown-command path:
//
//	error: unknown command "help"
//	  did you mean: figaro hup
//
// The most-guessed verb in the tool, answered with a suggestion to signal
// the daemon — at the exact moment the user is lost. cli.go even gated its
// update-check on `args[0] == "help"`, so the code already assumed the verb
// existed.
func TestHelpVerb(t *testing.T) {
	build := func(out, errOut *bytes.Buffer) *Router {
		r := NewRouter("figaro")
		r.Stdout, r.Stderr = out, errOut
		r.Register(&Command{Name: "send", Short: "Send a prompt", Long: "Send it.", Run: func(*RunContext) error { return nil }})
		r.Register(&Command{Name: "hup", Short: "Signal the daemon", Run: func(*RunContext) error { return nil }})
		return r
	}

	t.Run("bare help prints usage to stdout, exit 0", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := build(&out, &errOut).Run([]string{"help"}); code != 0 {
			t.Errorf("exit %d, want 0", code)
		}
		if !strings.Contains(out.String(), "Usage: figaro") {
			t.Errorf("stdout: %q", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr should be empty: %q", errOut.String())
		}
	})

	t.Run("help <command> prints that command", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := build(&out, &errOut).Run([]string{"help", "send"}); code != 0 {
			t.Errorf("exit %d, want 0", code)
		}
		if !strings.Contains(out.String(), "Usage: figaro send") {
			t.Errorf("stdout: %q", out.String())
		}
	})

	t.Run("help <typo> is misuse: stderr, exit 2, did-you-mean", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := build(&out, &errOut).Run([]string{"help", "snd"}); code != 2 {
			t.Errorf("exit %d, want 2", code)
		}
		if out.Len() != 0 {
			t.Errorf("stdout must stay clean on misuse: %q", out.String())
		}
		if !strings.Contains(errOut.String(), "did you mean: figaro help send") {
			t.Errorf("stderr: %q", errOut.String())
		}
	})

	t.Run("help is listed and completes", func(t *testing.T) {
		var out, errOut bytes.Buffer
		r := build(&out, &errOut)
		r.Run([]string{"--help"})
		if !strings.Contains(out.String(), "help") {
			t.Errorf("help verb missing from the listing: %q", out.String())
		}
		names := r.CommandNames()
		var found bool
		for _, n := range names {
			if n == "help" {
				found = true
			}
		}
		if !found {
			t.Errorf("CommandNames lacks help: %v", names)
		}
	})
}
