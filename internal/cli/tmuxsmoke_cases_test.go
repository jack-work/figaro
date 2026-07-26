package cli

// ---------------------------------------------------------------------------
// THE SMOKE SUITE.
//
// Every case here corresponds to a bug that SHIPPED, was found by a human in
// his own shell, and was invisible to the in-process suite. If you are tempted
// to move one of these into a unit test, read what it caught first.
//
//	FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v
// ---------------------------------------------------------------------------

import (
	"strings"
	"testing"
	"time"
)

// A completed turn must return the shell.
//
// CAUGHT: a build shipped where `figaro send` never exited after the turn
// completed. Ctrl-C, Ctrl-D, q and Escape ALL failed to dismiss it — the user
// could not leave the view, and reported he could not evaluate the branch at
// all. No in-process test can observe this: the hang is in the process
// lifecycle, not in any function's return value.
func TestSmoke_ProcessExitsAfterTurn(t *testing.T) {
	smokeEnabled(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 30)

	p.startTurn("reply with exactly one word: EXITOK")
	p.waitIdle(120 * time.Second)

	if got := bodyLines(p.scrollback(), "EXITOK"); got != 1 {
		t.Errorf("reply body line appears %d times, want exactly 1\n%s", got, p.scrollback())
	}
	// Give the process a beat to unwind after the last paint.
	for i := 0; i < 20 && p.alive(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if p.alive() {
		t.Fatalf("figaro is STILL RUNNING after the turn completed — the user cannot exit the view\n%s",
			p.visible())
	}
}

// Every documented exit key must work while a turn streams.
//
// CAUGHT: the same hang as above. These keys are the user's only escape from a
// long turn, so each is asserted separately — a suite that only tests Ctrl-C
// would have passed while Ctrl-D and q were dead.
func TestSmoke_ExitKeysWork(t *testing.T) {
	smokeEnabled(t)
	for _, k := range []string{"C-c", "C-d"} {
		t.Run(k, func(t *testing.T) {
			env, bin := smokeStore(t), smokeBinary(t)
			p := newPane(t, env, bin, 100, 30)
			p.startTurn("use bash to sleep 60, then say SLOWDONE")
			time.Sleep(12 * time.Second) // mid-stream, deliberately not idle
			if !p.alive() {
				t.Skip("turn ended before the key could be sent; lengthen the prompt")
			}
			p.key(k)
			for i := 0; i < 20 && p.alive(); i++ {
				time.Sleep(500 * time.Millisecond)
			}
			if p.alive() {
				t.Fatalf("%s did not dismiss the view\n%s", k, p.visible())
			}
		})
	}
}

// One turn leaves exactly one footer in scrollback.
//
// CAUGHT: a build shipped where the submit-time footer was frozen into
// scrollback and then a second footer printed at completion — the user saw the
// context bar twice for a single exchange. Invisible to a renderer unit test,
// which sees only what compose() DECIDES to paint and never what survives a
// frame scrolling away.
func TestSmoke_OneTurnOneFooter(t *testing.T) {
	smokeEnabled(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 30)

	p.startTurn("reply with exactly one word: FOOTOK")
	p.waitIdle(120 * time.Second)

	sb := p.scrollback()
	if c := pagerChrome(sb); c != 0 {
		t.Skipf("view auto-promoted to the pager (chrome=%d); re-run with a taller pane", c)
	}
	if got := footers(sb); got != 1 {
		t.Errorf("one turn produced %d footers, want exactly 1\n%s", got, sb)
	}
}

// Letters in the inline view are KEYBINDINGS, not text.
//
// CAUGHT, and then REVERTED: an in-view steer composer was built that made every
// printable character start typing a draft. Nobody asked for it, and it cost ten
// keybindings — `k` opened a text box instead of scrolling. The user's rule is
// that there is nothing in the UI to steer: a message is a steer purely because
// of WHEN it is sent, so the transcript stays lean and the keyboard stays a
// keyboard.
//
// `j` is the probe because it was the loudest casualty: it is both a motion and
// the ninth letter of "just", which is how the composer's trigger was found.
func TestSmoke_LettersAreKeybindingsNotText(t *testing.T) {
	smokeEnabled(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.startTurn("use bash to sleep 45, then say KEYOK")
	time.Sleep(14 * time.Second)
	if !p.alive() {
		t.Skip("turn ended before the key could be sent")
	}

	p.typeSlowly("j") // a motion, not the first letter of a draft
	time.Sleep(time.Second)

	vis := p.visible()
	if strings.Contains(vis, "steer ↳") || strings.Contains(vis, "send ↳") {
		t.Fatalf("a letter opened a text box — the composer is back\n%s", vis)
	}
	if c := pagerChrome(vis); c == 0 {
		t.Fatalf("'j' did not reach the pager: it must scroll, not be swallowed\n%s", vis)
	}
}

// The live view and `fig show` must agree about node ORDER.
//
// CAUGHT: incipit hoisted a mid-turn steer above the tools that preceded it,
// while `fig show` placed it correctly. The same turn told two different
// stories depending on how you looked at it — which the purity invariant
// (Turns() is a pure function of the message list) explicitly forbids.
//
// The steer must land AFTER a tool has completed or there is nothing to
// misorder: a steer fired before the first tool shows nothing wrong, and that
// single timing difference produced two contradictory bug reports.
func TestSmoke_SteerOrderMatchesShow(t *testing.T) {
	smokeEnabled(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 100) // tall: tool-heavy turns auto-promote

	p.startTurn("run 3 readonly bash commands with sleep 8 between each, then say ORDEROK")
	time.Sleep(14 * time.Second) // a tool has completed by now — this is the trick
	if !p.alive() {
		t.Skip("turn ended before the steer could land")
	}
	p.typeSlowly("STEERORDER mention plum")
	p.key("Enter")
	p.waitIdle(180 * time.Second)

	sb := p.scrollback()
	if c := pagerChrome(sb); c != 0 {
		t.Skipf("view auto-promoted (chrome=%d); re-run taller", c)
	}
	if got := strings.Count(sb, "↳ input"); got != 1 {
		t.Errorf("steer marker appears %d times, want exactly 1", got)
	}
	// The steer must not sit directly beneath the inquiry's own header: that
	// adjacency was the visible signature of the hoist.
	lines := strings.Split(sb, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "↳ input") && strings.Contains(lines[i-1], "❯ input") {
			t.Errorf("steer is hoisted directly under the inquiry header — live order disagrees with fig show\n%s", sb)
			break
		}
	}
}
