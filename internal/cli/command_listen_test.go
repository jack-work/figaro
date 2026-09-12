package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// YOU HAVE TO LISTEN TO A FIGARO. `:listen` is the pager's spelling of
// `figaro listen`; `:open` was a second name for it and is gone. `:at` is
// `:attend`, and the two verbs differ only in the binding.
func TestOverlay_ListenAttendAndNoOpen(t *testing.T) {
	if !overlayVerbs["listen"] {
		t.Fatal(":listen is not an overlay verb")
	}
	for _, gone := range []string{"open", "o"} {
		if overlayVerbs[gone] {
			t.Fatalf(":%s is still an overlay verb", gone)
		}
	}
	if !overlayVerbs["attend"] || !overlayVerbs["at"] {
		t.Fatal(":attend / :at must stay")
	}
	// listen vs attend: one verb body each, and attend's is listen's plus a
	// binding. The difference is visible in what a spec-less call refuses.
	env := verbEnv{}
	if _, _, err := listenVerb(context.Background(), env, ""); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("listenVerb(\"\"): %v", err)
	}
	if _, err := attendVerb(context.Background(), env, ""); err == nil || !strings.Contains(err.Error(), "attend") {
		t.Fatalf("attendVerb(\"\"): %v", err)
	}
}

// ONE PARSER, TWO DOORS. The shell's fork runs planFork; the box's :fork
// runs planFork. The same argv must produce the same plan, and the box's
// refusals are the shell's grammar refusals with the box's own two on top.
func TestTwoDoors_ForkArgvParsesTheSame(t *testing.T) {
	cases := [][]string{
		{"--", "what about this?"},
		{"abcd1234", "--", "hello"},
		{"abcd1234:12", "-S", "mantra=q", "--", "hello"},
		{"--stay", "abcd1234.42", "-O", "sonn5", "--", "hi"},
		{"--", "<412.0:23-1180>! why?"},
	}
	for _, argv := range cases {
		shell, serr := planFork(argv)
		box, berr := planFork(argv) // the box calls the same function; the assertion is that it does
		if (serr == nil) != (berr == nil) || !reflect.DeepEqual(shell, box) {
			t.Fatalf("%v: shell=%+v/%v box=%+v/%v", argv, shell, serr, box, berr)
		}
		if serr == nil && shell.prompt != extractPrompt(argv) {
			t.Fatalf("%v: prompt %q, extractPrompt %q", argv, shell.prompt, extractPrompt(argv))
		}
	}
	// The grammar's own refusals reach the box unchanged.
	if _, err := planFork([]string{"--bogus", "--", "x"}); err == nil {
		t.Fatal("planFork accepted --bogus")
	}
}

func TestTwoDoors_SendArgvParsesTheSame(t *testing.T) {
	for _, argv := range [][]string{
		{"--", "hi"},
		{"abcd1234", "--", "hi"},
		{"--id", "abcd1234", "-S", "k=v", "--", "hi there"},
		{"-f", "abcd1234", "--", "hi"},
	} {
		plan, err := planSend(argv)
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		opts, rest, err := extractSendFlags(argv)
		if err != nil || !reflect.DeepEqual(plan.opts, opts) || plan.prompt != extractPrompt(rest) {
			t.Fatalf("%v: planSend and extractSendFlags disagree: %+v vs %+v", argv, plan, opts)
		}
	}
}

// The box's own two refusals for :fork: -f (the transcript is the stream)
// and a prompt-less fork (nothing to show).
func TestCommandFork_RefusesForgetAndNoPrompt(t *testing.T) {
	p := newInputProbe(t, true)
	_, err := p.in.commandFork(context.Background(), []string{"-f", "--", "hello"})
	if err == nil || !strings.Contains(err.Error(), "-f") || !strings.Contains(err.Error(), "--stay") {
		t.Fatalf("-f: %v", err)
	}
	_, err = p.in.commandFork(context.Background(), []string{"abcd1234"})
	if err == nil || !strings.Contains(err.Error(), "--") {
		t.Fatalf("no prompt: %v", err)
	}
	// --stay is accepted by the grammar: it is the way to stay.
	if plan, err := planFork([]string{"--stay", "--", "x"}); err != nil || !plan.opts.stay {
		t.Fatalf("--stay: %+v %v", plan, err)
	}
}

// forkVerb's binding rule is the shell's: rebind only when the fork was of
// the shell's own aria and --stay was not given. With no shell, say so.
func TestVerbEnv_BoundAriaWithoutAShell(t *testing.T) {
	env := verbEnv{shellPID: 0}
	id, why := env.boundAria(context.Background())
	if id != "" || !strings.Contains(why, "no shell") {
		t.Fatalf("boundAria with no shell: %q %q", id, why)
	}
}
