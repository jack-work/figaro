package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// newTestTranscript is a pager wired to a plain writer, so the test reads the
// exact BYTES figaro would put on a terminal — which is the level this bug
// lives at. Content is irrelevant here; the escapes are the subject.
func newTestTranscript(out *strings.Builder) *transcript {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "q", Sealed: true}},
	}})
	return newTranscript(out, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
}

// THE COMPLAINT: "on exit figaro sometimes does not preserve scrollback… the
// previous scrollback is gone". Measured in a real pty (tmux -L fig-2747,
// `script -f` recording what figaro WRITES) at three pane rows:
//
//	?1049h (enter alt screen) = 0
//	?1049l (leave alt screen) = 1
//	\x1b[2J (erase screen)    = 1     <- on the PRIMARY screen. The user's own.
//
// At thirty rows the same drive gives 1 / 1 — paired, harmless.
//
// MECHANISM. enter() only QUEUES the switch (t.prefix, emitted by whichever
// frame paints next) and a frame is not guaranteed: renderFrame returns early
// below 4 rows, and render() can defer behind the frame-rate gate. leave()
// wrote the exit sequence unconditionally. So a pager that never painted still
// erased the screen it had never taken over.
//
// The pairing is now altPending (queued) -> altOn (emitted) -> leave.

// fakeGate is a render gate that always refuses, i.e. every frame is deferred.
// It stands for the second way a paint can fail to happen: the rate limiter.
func alwaysDefer() bool { return false }

func TestLeaveWithoutAPaintWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(tr *transcript)
	}{
		{
			// A pane under four rows: renderFrame returns before painting.
			name:  "pane too short to paint",
			setup: func(tr *transcript) { tr.h = 3 },
		},
		{
			// A frame deferred behind the rate gate, and the process exits
			// before the trailing flush. This is the "occasionally" case.
			name:  "frame deferred by the rate gate",
			setup: func(tr *transcript) { tr.h = 40; tr.gate = alwaysDefer },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			tr := newTestTranscript(&out)
			tc.setup(tr)

			tr.enter()
			if got := out.String(); strings.Contains(got, altScreenOn) {
				t.Fatalf("setup is wrong: this case must NOT paint, but it emitted the switch: %q", got)
			}
			out.Reset()

			tr.leave()

			got := out.String()
			if strings.Contains(got, "\x1b[2J") {
				t.Errorf("leave() erased a screen it never took over: %q", got)
			}
			if strings.Contains(got, altScreenOff) {
				t.Errorf("leave() switched away from an alt screen it never entered: %q", got)
			}
			if got != "" {
				t.Errorf("leave() must write nothing at all here, wrote %q", got)
			}
		})
	}
}

// The other half of the pairing: when the pager DID paint, leaving must still
// restore the terminal. A fix that simply stopped writing would pass the test
// above and strand every real user on the alt screen.
func TestLeaveAfterAPaintRestoresTheScreen(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.h = 40

	tr.enter()
	if got := out.String(); !strings.Contains(got, altScreenOn) {
		t.Fatalf("setup is wrong: this case must paint and switch, got %q", got)
	}
	out.Reset()

	tr.leave()
	got := out.String()
	if !strings.Contains(got, altScreenOff) {
		t.Fatalf("leave() left the user on the alt screen: %q", got)
	}
	// Mouse reporting off BEFORE the swap, so no \x1b[<…M leaks to the shell.
	if i, j := strings.Index(got, "\x1b[?1000"), strings.Index(got, altScreenOff); i >= 0 && i > j {
		t.Errorf("mouse reporting must be disabled before the swap: %q", got)
	}
}

// Idempotence: leave() twice must not write the exit sequence twice. A double
// ?1049l pops a screen the terminal never pushed.
func TestLeaveIsIdempotent(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.h = 40
	tr.enter()
	tr.leave()
	out.Reset()
	tr.leave()
	if got := out.String(); got != "" {
		t.Fatalf("second leave() wrote %q", got)
	}
}

// enter -> leave -> enter -> leave, the reopen path: each cycle must switch
// exactly once in each direction.
func TestEnterLeaveCyclesArePaired(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.h = 40
	for i := range 3 {
		tr.enter()
		tr.leave()
		got := out.String()
		if on, off := strings.Count(got, altScreenOn), strings.Count(got, altScreenOff); on != i+1 || off != i+1 {
			t.Fatalf("after %d cycles: %d enters, %d leaves", i+1, on, off)
		}
	}
}

// The erase at leave is gone on purpose: ?1049l restores the primary screen by
// definition. Keeping it destroyed the user's screen on any terminal that does
// not honour 1049 — which is precisely the reported Windows shape, where the
// pager's frames land in the primary buffer as ordinary text.
func TestLeaveDoesNotEraseTheScreen(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.h = 40
	tr.enter()
	out.Reset()
	tr.leave()
	if got := out.String(); strings.Contains(got, "\x1b[2J") {
		t.Fatalf("leave() still erases: %q", got)
	}
}

// os.Exit does not run defers, and two exits in stream.go are os.Exit: die()
// (reachable AFTER --listen has opened the pager — a Qua failure lands there)
// and the Ctrl-C 130 path. Both used to leave the terminal on the alternate
// screen, shell invisible. exitNow runs the registered hooks first.
func TestExitHooksRunBeforeTheProcessGoes(t *testing.T) {
	prevExit, prevHooks := exitProcess, exitHooks
	defer func() { exitProcess, exitHooks = prevExit, prevHooks }()

	var order []string
	exitHooks = nil
	exitProcess = func(int) { order = append(order, "exit") }
	atExit(func() { order = append(order, "first") })
	atExit(func() { order = append(order, "second") })

	exitNow(1)

	// LIFO like defers, and the process leaves last.
	want := []string{"second", "first", "exit"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("hook order: got %v, want %v", order, want)
	}
	if exitHooks != nil {
		t.Error("hooks must not run twice")
	}
}

// die() and dieUsage() must go through the hooks, not around them — that is
// the whole point, since die() is the exit that strands a pager.
func TestDieRunsExitHooks(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func()
		code int
	}{
		{"die", func() { die("boom") }, 1},
		{"dieUsage", func() { dieUsage("bad argv") }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevExit, prevHooks := exitProcess, exitHooks
			defer func() { exitProcess, exitHooks = prevExit, prevHooks }()
			restored, code := false, 0
			exitHooks = nil
			exitProcess = func(c int) { code = c }
			atExit(func() { restored = true })
			tc.call()
			if !restored {
				t.Error("the terminal-restore hook did not run")
			}
			if code != tc.code {
				t.Errorf("exit code %d, want %d", code, tc.code)
			}
		})
	}
}
