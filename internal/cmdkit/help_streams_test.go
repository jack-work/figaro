package cmdkit

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpStreams pins the one rule that separates the two writers: who
// asked.
//
// Before this, printUsage and printCommandHelp both opened with
// `w := r.Stderr`, so `figaro --help` and `figaro send --help` produced
// ZERO bytes on stdout — 3400 bytes of help, all on the diagnostic
// channel. `fig send --help | grep exec` printed nothing at all, which
// reads as a broken binary rather than a misrouted stream.
//
// The inverse must hold just as firmly: usage printed because argv was
// wrong is a diagnostic. It stays on stderr with a non-zero code so it
// can never pollute a pipeline.
func TestHelpStreams(t *testing.T) {
	newRouter := func(out, errOut *bytes.Buffer) *Router {
		r := NewRouter("figaro")
		r.Version = "9.9.9"
		r.Stdout, r.Stderr = out, errOut
		r.Register(&Command{
			Name:  "send",
			Short: "Send a prompt",
			Flags: []FlagDef{{Long: "id"}},
			Run:   func(ctx *RunContext) error { return nil },
		})
		return r
	}

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // substring required on stdout ("" = stdout must be empty)
		wantErr  string // substring required on stderr ("" = stderr must be empty)
	}{
		// Asked for it: stdout, exit 0.
		{"top-level --help", []string{"--help"}, 0, "Usage: figaro", ""},
		{"top-level -h", []string{"-h"}, 0, "Usage: figaro", ""},
		{"command --help", []string{"send", "--help"}, 0, "Usage: figaro send", ""},
		{"version", []string{"--version"}, 0, "figaro 9.9.9", ""},
		{"version short", []string{"-V"}, 0, "figaro 9.9.9", ""},

		// Did not ask for it: stderr, non-zero, stdout untouched.
		{"no args", nil, 2, "", "Usage: figaro"},
		{"unknown command", []string{"snd"}, 2, "", `unknown command "snd"`},
		{"parse error", []string{"send", "--bogus"}, 2, "", "unknown flag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := newRouter(&out, &errOut).Run(tc.args)
			if code != tc.wantCode {
				t.Errorf("exit code: got %d, want %d", code, tc.wantCode)
			}
			checkStream(t, "stdout", out.String(), tc.wantOut)
			checkStream(t, "stderr", errOut.String(), tc.wantErr)
		})
	}
}

func checkStream(t *testing.T, name, got, want string) {
	t.Helper()
	if want == "" {
		if got != "" {
			t.Errorf("%s: want empty, got %d bytes: %.60q", name, len(got), got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("%s: want it to contain %q, got %.120q", name, want, got)
	}
}

// TestHelpStreamsNilWriters guards the zero-value Router: a struct literal
// (no NewRouter) must still print rather than panic on a nil io.Writer.
func TestHelpStreamsNilWriters(t *testing.T) {
	r := &Router{Name: "figaro", index: map[string]*Command{}}
	if r.outw() == nil || r.errw() == nil {
		t.Fatal("nil writers must resolve to the os defaults")
	}
}
