package cli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// `:listen` is `figaro listen`; `:at` is `:attend`; there is no `:open`.
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
	env := verbEnv{}
	if _, _, err := listenVerb(context.Background(), env, ""); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("listenVerb(\"\"): %v", err)
	}
	if _, err := attendVerb(context.Background(), env, ""); err == nil || !strings.Contains(err.Error(), "attend") {
		t.Fatalf("attendVerb(\"\"): %v", err)
	}
}

// Both doors call prepareFork; only the surface differs.
func TestForkPreparationDiffersOnlyByTheSurface(t *testing.T) {
	for _, line := range []string{
		"fork -- what about this?",
		"fork abcd1234 -- hello",
		"fork abcd1234:12 -S mantra=q -- hello",
		"fork --stay abcd1234.42 -O sonn5 -- hi",
		"fork -- <412.0:23-1180>! why?",
	} {
		argv := tokenize(line)[1:]
		shell, serr := prepareFork(argv, shellSurface)
		pager, perr := prepareFork(argv, pagerSurface)
		if serr != nil || perr != nil {
			t.Fatalf("%q: shell=%v pager=%v", line, serr, perr)
		}
		if !reflect.DeepEqual(shell, pager) {
			t.Fatalf("%q: the two surfaces prepared different plans:\n%+v\n%+v", line, shell, pager)
		}
		if shell.prompt != extractPrompt(argv) {
			t.Fatalf("%q: prompt %q, extractPrompt %q", line, shell.prompt, extractPrompt(argv))
		}
	}
	if _, err := prepareFork([]string{"--bogus", "--", "x"}, pagerSurface); err == nil {
		t.Fatal("prepareFork accepted --bogus")
	}
}

// A flag the surface cannot honour is refused by name, by both prompt verbs.
func TestPagerRefusesWhatItCannotHonour(t *testing.T) {
	for _, argv := range [][]string{
		{"-x", "--", "list the files"},
		{"-n", "-x", "--", "hi"},
		{"-r", "--", "hi"},
		{"-v", "--", "hi"},
		{"-j", "--", "hi"},
		{"-l", "--", "hi"},
		{"--record", "/tmp/t.tape", "--", "hi"},
	} {
		if _, err := prepareSend(argv, pagerSurface); err == nil {
			t.Errorf("send %v was accepted by the pager", argv)
		}
		if _, err := prepareFork(argv, pagerSurface); err == nil {
			t.Errorf("fork %v was accepted by the pager", argv)
		}
		if _, err := prepareFork(argv, shellSurface); err != nil {
			t.Errorf("fork %v refused at a shell: %v", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"--", "hi"},
		{"abcd1234", "--", "hi"},
		{"-S", "mantra=x", "-O", "sonn5", "--", "hi"},
		{"--stay", "--", "hi"},
	} {
		if _, err := prepareFork(argv, pagerSurface); err != nil {
			t.Errorf("fork %v: %v", argv, err)
		}
	}
}

// A fork from the pager attends the branch, even under `:listen B` while bound to A.
func TestForkBindingIntent(t *testing.T) {
	cases := []struct {
		name   string
		intent bindIntent
		stay   bool
		bound  string
		target string
		move   bool
	}{
		{"pager forks the aria it shows, bound elsewhere", bindBranch, false, "A", "B", true},
		{"pager forks with --stay", bindBranch, true, "A", "B", false},
		{"pager with nothing bound", bindBranch, false, "", "B", true},
		{"shell forks its own", bindFanOut, false, "A", "A", true},
		{"shell fans out", bindFanOut, false, "A", "B", false},
		{"shell unbound", bindFanOut, false, "", "B", false},
		{"stay never moves", bindStay, false, "A", "A", false},
	}
	for _, c := range cases {
		move, note := bindDecision(c.intent, c.stay, c.bound, "no binding", c.target)
		if move != c.move {
			t.Errorf("%s: move=%v, want %v (note %q)", c.name, move, c.move, note)
		}
		if !move && c.intent != bindStay && !c.stay && note == "" {
			t.Errorf("%s: refused to attend and said nothing", c.name)
		}
	}
}

// In the pager nothing streams for -f to forget: on fork it keeps attendance
// where it is, as --stay does, with or without a prompt; on send it is taken
// and ignored. At a shell -f is still forget.
func TestThePagerTakesFAsStay(t *testing.T) {
	for _, argv := range [][]string{{"-f", "--", "hi"}, {"-f"}, {"--forget", "abcd1234"}, {"--stay", "--", "hi"}} {
		plan, err := prepareFork(argv, pagerSurface)
		if err != nil || !plan.opts.stay || plan.opts.forget {
			t.Errorf("pager fork %v: stay=%v forget=%v err=%v, want stay", argv, plan.opts.stay, plan.opts.forget, err)
		}
	}
	plan, err := prepareFork([]string{"--", "hi"}, pagerSurface)
	if err != nil || plan.opts.stay {
		t.Errorf("pager fork without -f: stay=%v err=%v, want it to attend", plan.opts.stay, err)
	}
	if plan, err := prepareFork([]string{"-f", "--", "hi"}, shellSurface); err != nil || !plan.opts.forget || plan.opts.stay {
		t.Errorf("shell fork -f: forget=%v stay=%v err=%v, want forget", plan.opts.forget, plan.opts.stay, err)
	}
	send, err := prepareSend([]string{"-f", "--", "hi"}, pagerSurface)
	if err != nil || send.opts.forget || send.prompt != "hi" {
		t.Errorf("pager send -f: %+v %v, want it taken and ignored", send, err)
	}
}

func TestVerbEnv_BoundAriaWithoutAShell(t *testing.T) {
	env := verbEnv{shellPID: 0}
	id, why := env.boundAria(context.Background())
	if id != "" || !strings.Contains(why, "no shell") {
		t.Fatalf("boundAria with no shell: %q %q", id, why)
	}
}

// `:attend a b` must not ask for an aria called "a b".
func TestOverlaySpecIsOnePositional(t *testing.T) {
	if spec, err := oneSpec("attend", []string{"abcd1234"}); err != nil || spec != "abcd1234" {
		t.Fatalf("one spec: %q %v", spec, err)
	}
	if spec, err := oneSpec("attend", nil); err != nil || spec != "" {
		t.Fatalf("no spec: %q %v", spec, err)
	}
	if _, err := oneSpec("attend", []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "one aria") {
		t.Fatalf("two words were accepted as one aria: %v", err)
	}
}

// commandFork is driven with one ':' line; what is asserted is the request a
// daemon receives.
func TestCommandForkSendsThePlansRequest(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "angelus.sock")
	got := make(chan rpc.ForkRequest, 2)
	fakeRPCServer(t, sock, map[string]jkrpc.HandlerFunc{
		rpc.MethodResolve: func(context.Context, json.RawMessage) (interface{}, error) {
			return rpc.ResolveResponse{Found: true, FigaroID: "abcd1234"}, nil
		},
		rpc.MethodFork: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			var req rpc.ForkRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			got <- req
			return nil, errors.New("fork stops here")
		},
	})
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(sock))
	if err != nil {
		t.Fatalf("dial the fake angelus: %v", err)
	}
	defer acli.Close()

	in := &interactiveInput{mu: new(renderLock), acli: acli, figaroID: "abcd1234"}
	if _, err := in.commandFork(context.Background(), tokenize("fork abcd1234:12 -S mantra=q -- hello")[1:]); err == nil {
		t.Fatal("the fake angelus refused the fork and commandFork reported success")
	}

	select {
	case req := <-got:
		if req.FigaroID != "abcd1234" || req.AtTurn != 12 {
			t.Fatalf("the box forked %s at turn %d", req.FigaroID, req.AtTurn)
		}
		if req.Patch == nil {
			t.Fatal("the box's fork carried no dressing")
		}
		if v := string(mustEntry(*req.Patch, "mantra")); v != `"q"` {
			t.Fatalf("the box's fork wore %s", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake angelus was never asked to fork")
	}
}
