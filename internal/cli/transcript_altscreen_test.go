package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

func newTestTranscript(out *strings.Builder) *transcript {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "q", Sealed: true}},
	}}, aria.Notify)
	return newTranscript(out, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
}

// enter() only queues the alt-screen switch (t.prefix, emitted by the next
// frame), and a frame is not guaranteed: renderFrame returns early below 4
// rows and render() can defer behind the gate. leave() must write nothing
// unless the switch was emitted.

func alwaysDefer() bool { return false }

func TestLeaveWithoutAPaintWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(tr *transcript)
	}{
		{
			name:  "pane too short to paint",
			setup: func(tr *transcript) { tr.h = 3 },
		},
		{
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
	// Mouse reporting off before the swap, or \x1b[<…M leaks to the shell.
	if i, j := strings.Index(got, "\x1b[?1000"), strings.Index(got, altScreenOff); i >= 0 && i > j {
		t.Errorf("mouse reporting must be disabled before the swap: %q", got)
	}
}

// A double ?1049l pops a screen the terminal never pushed.
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

// ?1049l restores the primary screen; an erase destroys it on terminals that
// do not honour 1049.
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

// os.Exit does not run defers; exitNow runs the registered hooks first.
func TestExitHooksRunBeforeTheProcessGoes(t *testing.T) {
	prevExit, prevHooks := exitProcess, exitHooks
	defer func() { exitProcess, exitHooks = prevExit, prevHooks }()

	var order []string
	exitHooks = nil
	exitProcess = func(int) { order = append(order, "exit") }
	atExit(func() { order = append(order, "first") })
	atExit(func() { order = append(order, "second") })

	// exitNow panics; the boundary (RecoverExit, as in cli.Run) runs the hooks.
	func() {
		defer RecoverExit()
		exitNow(1)
	}()

	want := []string{"second", "first", "exit"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("hook order: got %v, want %v", order, want)
	}
	if exitHooks != nil {
		t.Error("hooks must not run twice")
	}
}

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
			func() {
				defer RecoverExit()
				tc.call()
			}()
			if !restored {
				t.Error("the terminal-restore hook did not run")
			}
			if code != tc.code {
				t.Errorf("exit code %d, want %d", code, tc.code)
			}
		})
	}
}
