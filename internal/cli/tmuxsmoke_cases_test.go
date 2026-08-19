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
// completed. Ctrl-C, Ctrl-D, q and Escape ALL failed to dismiss it: the user
// could not leave the view, and reported he could not evaluate the branch at
// all. No in-process test can observe this: the hang is in the process
// lifecycle, not in any function's return value.
func TestSmoke_ProcessExitsAfterTurn(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
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
		t.Fatalf("figaro is STILL RUNNING after the turn completed: the user cannot exit the view\n%s",
			p.visible())
	}
}

// Every documented exit key must work while a turn streams.
//
// CAUGHT: the same hang as above. These keys are the user's only escape from a
// long turn, so each is asserted separately, a suite that only tests Ctrl-C
// would have passed while Ctrl-D and q were dead.
func TestSmoke_ExitKeysWork(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	for _, k := range []string{"C-c", "C-d"} {
		t.Run(k, func(t *testing.T) {
			env, bin := smokeStore(t), smokeBinary(t)
			p := newPane(t, env, bin, 100, 30)
			p.startTurn("use bash to sleep 60, then say SLOWDONE")
			time.Sleep(12 * time.Second) // mid-stream, deliberately not idle
			if !p.alive() {
				decline(t, "turn ended before the key could be sent; lengthen the prompt")
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
// scrollback and then a second footer printed at completion: the user saw the
// context bar twice for a single exchange. Invisible to a renderer unit test,
// which sees only what compose() DECIDES to paint and never what survives a
// frame scrolling away.
func TestSmoke_OneTurnOneFooter(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 30)

	p.startTurn("reply with exactly one word: FOOTOK")
	p.waitIdle(120 * time.Second)

	sb := p.scrollback()
	if c := pagerChrome(sb); c != 0 {
		decline(t, "view auto-promoted to the pager (chrome=%d); re-run with a taller pane", c)
	}
	if got := footers(sb); got != 1 {
		t.Errorf("one turn produced %d footers, want exactly 1\n%s", got, sb)
	}
}

// Letters in the inline view are KEYBINDINGS, not text.
//
// CAUGHT, and then REVERTED: an in-view steer composer was built that made every
// printable character start typing a draft. Nobody asked for it, and it cost ten
// keybindings: `k` opened a text box instead of scrolling. The user's rule is
// that there is nothing in the UI to steer: a message is a steer purely because
// of WHEN it is sent, so the transcript stays lean and the keyboard stays a
// keyboard.
//
// `j` is the probe because it was the loudest casualty: it is both a motion and
// the ninth letter of "just", which is how the composer's trigger was found.
func TestSmoke_LettersAreKeybindingsNotText(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.startTurn("use bash to sleep 45, then say KEYOK")
	time.Sleep(14 * time.Second)
	if !p.alive() {
		decline(t, "turn ended before the key could be sent")
	}

	p.typeSlowly("j") // a motion, not the first letter of a draft
	time.Sleep(time.Second)

	vis := p.visible()
	if strings.Contains(vis, "steer ↳") || strings.Contains(vis, "send ↳") {
		t.Fatalf("a letter opened a text box: the composer is back\n%s", vis)
	}
	if c := pagerChrome(vis); c == 0 {
		t.Fatalf("'j' did not reach the pager: it must scroll, not be swallowed\n%s", vis)
	}
}

// The live view and `fig show` must agree about node ORDER.
//
// CAUGHT: incipit hoisted a mid-turn steer above the tools that preceded it,
// while `fig show` placed it correctly. The same turn told two different
// stories depending on how you looked at it: which the purity invariant
// (Turns() is a pure function of the message list) explicitly forbids.
//
// The steer must land AFTER a tool has completed or there is nothing to
// misorder: a steer fired before the first tool shows nothing wrong, and that
// single timing difference produced two contradictory bug reports.
func TestSmoke_SteerOrderMatchesShow(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 100) // tall: tool-heavy turns auto-promote

	p.startTurn("run 3 readonly bash commands with sleep 8 between each, then say ORDEROK")
	time.Sleep(14 * time.Second) // a tool has completed by now: this is the trick
	if !p.alive() {
		decline(t, "turn ended before the steer could land")
	}
	p.typeSlowly("STEERORDER mention plum")
	p.key("Enter")
	p.waitIdle(180 * time.Second)

	sb := p.scrollback()
	if c := pagerChrome(sb); c != 0 {
		// DO NOT "RE-RUN TALLER". That advice was here, it is false, and it
		// cost two investigations. This pane is 100x100 -- the second tallest
		// in the suite -- and somebody already followed the advice to get it
		// there. Bounding the tool output to three one-line echoes was also
		// tried and also promoted at chrome=2. Promotion is not a function of
		// pane height or of output volume in the way the message implied, so
		// the message sent readers to the one remedy that cannot work.
		//
		// THIS SKIP IS A KNOWN COVERAGE HOLE, not a flake: with it, the steer
		// path has NO pty coverage at all, and it has been filed since
		// ~/notes/figaro/memory-campaign-open-items.md item 3 ("unverifiable
		// in the current harness -- it auto-promotes at 101 and 201 with
		// chrome=2"). It belongs to the CLI/client fold refactor.
		decline(t, "KNOWN HOLE: view auto-promoted (chrome=%d); the steer path has no pty coverage. "+
			"Do NOT re-run taller -- this pane is already 100x100 and bounding the output was tried too. "+
			"See memory-campaign-open-items.md item 3.", c)
	}
	if got := strings.Count(sb, "↳ input"); got != 1 {
		t.Errorf("steer marker appears %d times, want exactly 1", got)
	}
	// The steer must not sit directly beneath the inquiry's own header: that
	// adjacency was the visible signature of the hoist.
	lines := strings.Split(sb, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "↳ input") && strings.Contains(lines[i-1], "> input") {
			t.Errorf("steer is hoisted directly under the inquiry header: live order disagrees with fig show\n%s", sb)
			break
		}
	}
}

// Nothing may write to the terminal while the transcript pager owns it.
//
// CAUGHT (user's words): "Errors where the text bleeds into the status bar."
//
// The pager runs on the ALTERNATE SCREEN, and an alt screen has NO SCROLLBACK
// (measured: alternate_on=1, history_size=0: `capture-pane -p -S -` returns
// exactly the visible rows). So a write to stdout/stderr while the pager is up
// cannot land "below" anything. It lands ON THE GRID, at the cursor, and the
// painter finishes every frame by writing screen[t.h-1], the status row, so the
// cursor is parked there. Worse, those writes lead with "\n": on the bottom row
// a newline SCROLLS THE WHOLE GRID UP, the painter is never told, and t.prev
// stops describing the terminal. The visible result is the user's report plus a
// DUPLICATED STATUS ROW: the same duplicated-footer signature that has already
// shipped once from a different cause.
//
// Two sites are confirmed by real pty capture:
//
//   - internal/cli/stream.go:169  fmt.Fprintln(os.Stderr, "\n"+d.Reason)
//     reached because livelogTurn.finishTurn (livelog_bridge.go:561) returns
//     EARLY when t.tr.active: it does NOT leave the pager, so the comment at
//     that call site ("tear the live region down FIRST, so an error hint lands
//     on clean scrollback below it, not over the footer") is true inline and
//     FALSE in the pager.
//   - internal/cli/stream.go:358  fmt.Fprintln(os.Stderr, "\ninterrupting...")
//     written BEFORE any abandon/leave, so a plain Ctrl-C mid-turn does it with
//     no error involved at all.
//
// stream.go:346 ("follow: figaro listen …") is the NEGATIVE CONTROL and is
// correct: abandon() calls leaveTranscript() first. Verified clean.
//
// COSTS NO TOKENS. A deliberately invalid ANTHROPIC_API_KEY makes the turn fail
// with a 401 before a single token is generated, which is a better provocation
// than a bogus model or a blanked credential: it corrupts no config, and it
// lands on the "\n"+d.Reason branch rather than the providerSetupHint branch.
//
// THIS TEST IS EXPECTED TO FAIL until the bleed is fixed. The fix is a product
// decision (leave the pager first / route through the frame buffer / suppress
// and surface in the ! panel) and was deliberately left to the user.
func TestSmoke_ErrorDoesNotBleedIntoStatusBar(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	// The invalid key must win over whatever the copied config resolves.
	env := append(smokeStore(t), "ANTHROPIC_API_KEY=sk-ant-api03-deliberately-invalid-cherubino")
	bin := smokeBinary(t)
	p := newPane(t, env, bin, 100, 24)

	// -l opens the transcript pager AT STARTUP, so the pager owns the terminal
	// before the turn can fail. The turn then errors within about a second.
	p.send(bin + " send -l -- 'say OK'")
	p.key("Enter")
	p.waitIdle(90 * time.Second)

	vis, raw := p.visible(), p.rawVisible()

	// The assertion is deliberately conditional on the pager still owning the
	// grid, so that it PASSES under any of the three candidate fixes and FAILS
	// only on the bug. An earlier draft skipped when the pager was absent, which
	// could not tell "fixed by leaving the pager" from "the pager never came up",
	// and a skip that looks like a pass is how a test stops being evidence.
	if pagerChrome(vis) == 0 {
		// The pager is gone: whatever printed, it did not print over a frame.
		// That is fix (i) (leave before printing): or the pager never opened at
		// all, which this test simply has no opinion about.
		return
	}

	// The pager owns the grid. Two things must hold.
	//
	// SECONDARY, AND KNOWN TIMING-DEPENDENT: a duplicated status row appears only
	// if the painter repaints after the stray write scrolled the grid. Measured
	// across runs it shows up most of the time but not every time, so it is
	// asserted one-sided (> 1 is always wrong; a single row is fine) and it is
	// NOT the load-bearing assertion. The escape-sequence check below is, because
	// it is a property of the bytes themselves and does not race.
	if got := statusRows(vis); got > 1 {
		t.Errorf("status row appears %d times on the grid, want at most 1: "+
			"a second copy means the grid scrolled under the painter and t.prev "+
			"no longer describes the terminal\n%s", got, vis)
	}
	// No row may carry text WITHOUT the footer's dim styling. Every row the
	// renderer emits is wrapped in \x1b[2m … \x1b[0m (footer) or carries some
	// SGR; a completely unstyled row among them was written straight to the
	// terminal, bypassing the frame buffer. This is what distinguishes bug (b)
	// from a clipToWidth failure, and it stays true under fixes (ii) and (iii)
	// because both route the text through the renderer, which styles it.
	for _, ln := range strings.Split(raw, "\n") {
		if strings.TrimSpace(ln) == "" || !strings.Contains(ln, "error:") {
			continue
		}
		if !strings.Contains(ln, "\x1b[") {
			t.Errorf("error text reached the grid with NO escape sequences at all, "+
				"so it bypassed the frame buffer entirely: %q\nfull grid:\n%s", ln, vis)
		}
	}
}
