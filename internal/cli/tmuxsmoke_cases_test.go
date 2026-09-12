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
	"regexp"
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

// THE FAILED TURN'S CLOSER IS ITS BAR, in the incipit exactly as in the pager.
//
// What a 401 before the first token left in scrollback, byte-traced 2026-09-10:
// the question under a plain rule, no verdict, no reason, and then a
// `<aria>/runtime` patch re-opened the frozen question as a live region and
// parked the cursor at its top, so the shell prompt printed into the middle of
// it. Exit status 0. Four defects, one screen.
//
// This test asserts the shape a reader should be left with, in the terminal
// itself: one status row carrying the reason IN RED, the ✗, the aria id; the
// shell prompt BELOW that row, not inside the stanza; no second copy of the
// question; and an exit status that says the turn failed. The pager sibling
// above asserts the bytes never bypass the frame buffer; this one asserts what
// the frame buffer commits when the session ends inline.
//
// COSTS NO TOKENS, for the same reason as its sibling.
func TestSmoke_FailedTurnLeavesItsBarInScrollback(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env := append(smokeStore(t), "ANTHROPIC_API_KEY=sk-ant-api03-deliberately-invalid-cherubino")
	bin := smokeBinary(t)
	p := newPane(t, env, bin, 100, 30)

	p.send(bin + " send -- 'say OK'; echo EXIT=$?")
	p.key("Enter")
	p.waitIdle(90 * time.Second)

	sb, raw := p.scrollback(), p.tmuxOut("capture-pane", "-p", "-e", "-S", "-")
	if pagerChrome(sb) != 0 {
		decline(t, "the turn promoted to the pager; this case is about the incipit")
	}
	lines := strings.Split(strings.TrimRight(sb, "\n"), "\n")

	// The bar row: the reason leads, the verdict and the aria follow, on ONE row.
	bar := -1
	for i, ln := range lines {
		if strings.Contains(ln, "error:") && strings.Contains(ln, "✗") {
			bar = i
		}
	}
	if bar < 0 {
		t.Fatalf("no status row carries both the reason and the ✗:\n%s", sb)
	}
	if n := strings.Count(sb, "error:"); n != 1 {
		t.Errorf("the reason appears %d times, want exactly once (the bar):\n%s", n, sb)
	}
	// Red, and painted: the reason must arrive through the bar's alert path,
	// which is the only path that colours it. A gray reason beside a red ✗ is
	// the bug where report() posted trouble as a confirmation.
	for _, ln := range strings.Split(raw, "\n") {
		if strings.Contains(ln, "error:") && !strings.Contains(ln, "38;5;167") {
			t.Errorf("the reason is on the bar but not painted red: %q", ln)
		}
	}
	// The shell came back BELOW the stanza. EXIT= is printed by the shell after
	// figaro exits; anything of ours after it was painted over a returned prompt.
	exitAt := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "EXIT=") {
			exitAt = i
		}
	}
	if exitAt < 0 {
		t.Fatalf("the shell never came back:\n%s", sb)
	}
	if exitAt < bar {
		t.Errorf("the shell prompt (line %d) is ABOVE the bar (line %d): figaro painted after it exited:\n%s", exitAt, bar, sb)
	}
	for _, ln := range lines[exitAt+1:] {
		if strings.Contains(ln, "say OK") || strings.Contains(ln, "─────") {
			t.Errorf("conversation rows below the returned shell prompt:\n%s", sb)
			break
		}
	}
	if !strings.Contains(sb, "EXIT=1") {
		t.Errorf("a failed turn must exit 1; got %q", lines[exitAt])
	}
	// The question was frozen once. Two copies is the re-opened region.
	if n := bodyLines(sb, "say OK"); n != 1 {
		t.Errorf("the question appears %d times as a body line, want 1:\n%s", n, sb)
	}
}

// THE MESSAGE MUST BE HELD IN THE DRAWER UNTIL IT ROUND-TRIPS, and the status
// bar must say something DIFFERENT from "thinking" while it is in transit.
//
// The reported bug, in the author's words: "when sending a message in the
// transcript tui from `:send --`, upon sending it, the message takes some time
// before it roundtrips the transcript."
//
// It was not slowness. Between the drain loop lifting a message and the turn
// frame carrying it, the message was in NEITHER the queue nor the transcript --
// it existed nowhere a client could see, because the queue was PULLED twice a
// second and the lifted state was never published at all. So the fix is not a
// faster wire, it is a queue that says where a message is at every instant.
//
// A pane test is the only honest instrument here. The property is "what a
// reader sees between two events", and a unit test over compose() can only say
// what the client DECIDED to paint, not what stood on the screen.
func TestSmoke_QueuedMessageIsHeldUntilItRoundTrips(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A turn long enough that a second message lands BEHIND it: a send into an
	// idle aria never queues, and there would be nothing to hold.
	p.startTurn("use bash to sleep 40, then say FIRSTOK")
	time.Sleep(12 * time.Second)
	if !p.alive() {
		decline(t, "the first turn ended before a message could be queued behind it")
	}

	// `:send --` is the reported path, exactly.
	p.typeSlowly(":send -- QUEUETOK")
	p.key("Enter")

	// THE ASSERTION IS PROMPT. The whole complaint is about the interval right
	// after the send, so waiting for the turn would test the wrong moment.
	deadline := time.Now().Add(20 * time.Second)
	var sawRow, sawTransit string
	for time.Now().Before(deadline) {
		vis := p.visible()
		if sawRow == "" && strings.Contains(vis, "QUEUETOK") {
			sawRow = vis
		}
		// The bar must animate in a family DISTINCT from the thinking
		// spinner while the message is in transit.
		if sawTransit == "" && strings.ContainsAny(vis, string(sendingFrames)+string(acceptedFrames)) {
			sawTransit = vis
		}
		if sawRow != "" && sawTransit != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	if sawRow == "" {
		t.Errorf("the queued message never appeared on screen. It was accepted by the "+
			"daemon and shown nowhere: which is the reported bug exactly\n%s", p.visible())
	}
	if sawTransit == "" {
		t.Errorf("no in-transit indicator appeared. The bar must be animated and "+
			"DISTINCT from the thinking spinner between submit and the daemon's "+
			"acknowledgement\n%s", p.visible())
	}

	// And it must still be there a beat later: "held until roundtripped" means
	// the row does not blink away the instant the drain loop lifts it.
	time.Sleep(2 * time.Second)
	if vis := p.visible(); sawRow != "" && !strings.Contains(vis, "QUEUETOK") {
		t.Errorf("the queued message VANISHED before its turn reached the transcript. "+
			"That gap is the bug: the message is in neither the queue nor the "+
			"conversation, and the reader is left with nothing\n%s", vis)
	}
}

// The queue drawer must stay CURRENT WHILE IT IS OPEN.
//
// It used to be polled off the pager clock, so an open drawer was up to half a
// second stale and `:send` had to kick a manual refresh to paper over it. The
// queue is a intrinsic form now -- pushed as form patches over the connection the
// session already holds -- so the drawer follows without asking.
//
// THE SECOND MESSAGE COMES FROM OUTSIDE THE PANE, and that is the whole design
// of this case. Two earlier drafts sent it with `:send` from inside and both
// tested their own keystrokes instead of the product:
//
//   - the first pressed `Q` to open the drawer, not knowing that `:send` into
//     a busy aria opens it already (commandSend calls openQueueFromKey when
//     the daemon reports the turn active) -- so the `Q` TOGGLED IT SHUT and
//     the case declined saying the drawer never opened.
//   - the second typed `:send -- …` with the drawer open, and Enter was
//     consumed by the pit rather than submitting the command box. Measured:
//     the composer still held the text at the moment of failure.
//
// Neither had anything to do with whether the drawer follows the queue. An
// external sender removes the client's own input from the question entirely
// and asserts the only thing that matters: a message this client did not type
// appears in a drawer this client already has open.
func TestSmoke_OpenQueueDrawerStaysCurrent(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.startTurn("use bash to sleep 45, then say DRAWEROK")
	time.Sleep(12 * time.Second)
	if !p.alive() {
		decline(t, "the turn ended before the drawer could be opened")
	}

	// The aria on screen, read off the status row: the pane owns the session,
	// so this is the only place its id is written down.
	aria := ariaIDOnScreen(p.visible())
	if aria == "" {
		decline(t, "could not read the aria id off the status row\n%s", p.visible())
	}

	// One message from inside, purely to OPEN the drawer the way a user does.
	p.typeSlowly(":send -- DRAWERONE")
	p.key("Enter")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(p.visible(), "DRAWERONE") {
		time.Sleep(200 * time.Millisecond)
	}
	if vis := p.visible(); !strings.Contains(vis, "DRAWERONE") {
		decline(t, "the drawer never opened on the first message\n%s", vis)
	}

	// AND ONE FROM OUTSIDE, with the drawer already open. Nothing in this pane
	// knows it happened; the only way it can appear is if the daemon pushed it.
	figCmd(t, env, bin, "send", "-f", "--id", aria, "--", "DRAWERTWO")

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		vis := p.visible()
		if strings.Contains(vis, "DRAWERTWO") {
			// Both at once: the drawer STAYED open and grew, rather than being
			// rebuilt around only the newest message.
			if !strings.Contains(vis, "DRAWERONE") {
				t.Errorf("the second message replaced the first instead of joining it "+
					"in the open drawer\n%s", vis)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("a message queued by ANOTHER client never appeared in this one's open "+
		"drawer. The drawer is not following the queue; it is waiting to be asked\n%s",
		p.visible())
}

// ariaIDOnScreen picks the 8-hex aria id out of a status row. The row is
// `notice · state · <id> · mantra`, and the id is the only bare 8-hex token on
// it -- a mantra could contain one, so this takes the FIRST, which is the
// field's position.
func ariaIDOnScreen(capture string) string {
	re := regexp.MustCompile(`\b[0-9a-f]{8}\b`)
	for _, line := range strings.Split(capture, "\n") {
		if !strings.Contains(line, "·") {
			continue
		}
		if m := re.FindString(line); m != "" {
			return m
		}
	}
	return ""
}

// VISUAL MODE, END TO END: v puts a cursor up with no wash; v again anchors a
// highlight; hjkl extend it across a node boundary; `:` opens the box holding
// the range placeholder; y yanks the visible text; Y yanks a qualified
// coordinate that names a real LT of this aria.
//
// A pane test because the property is what the reader SEES: the cursor cell,
// the wash, the box's contents, the status row's yank note. The raw capture
// (-e) is read for the palette's own SGR bodies, so a wash that painted in
// the wrong colour, or a cursor that did not paint, fails here and nowhere
// else. The turn is kept short and the session is opened with -l so the
// pager is still up after the reply lands.
func TestSmoke_VisualCursorHighlightsAndYanks(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.send(bin + " send -l -- 'say the single word VISOK and nothing else'")
	p.key("Enter")
	p.waitIdle(120 * time.Second)
	if !p.alive() {
		decline(t, "the session did not stay open under -l")
	}
	if !strings.Contains(p.visible(), "VISOK") {
		decline(t, "the reply never landed on screen")
	}
	// tmux re-encodes SGR per cell and splits a combined sequence, so the
	// palette's bodies are matched by their BACKGROUND parameter alone, in
	// either spelling: the pane decides truecolour for itself.
	has := func(raw string, params ...string) bool {
		for _, p := range params {
			if strings.Contains(raw, "\x1b["+p+"m") {
				return true
			}
		}
		return false
	}
	washed := func(raw string) bool { return has(raw, "48;2;45;79;103", "48;5;23") }
	cursored := func(raw string) bool { return has(raw, "48;2;220;215;186", "48;5;187") }

	// v: a cursor, and no wash.
	p.typeSlowly("v")
	time.Sleep(400 * time.Millisecond)
	raw := p.rawVisible()
	if !cursored(raw) {
		t.Fatalf("v did not paint a cursor cell:\n%s", raw)
	}
	if washed(raw) {
		t.Fatalf("v painted a wash before any highlight was anchored:\n%s", raw)
	}
	t.Logf("after v:\n%s", raw)

	// v again: the highlight anchors; k extends it up across the node
	// boundary (the cursor seeds on the reply, k reaches the question).
	p.typeSlowly("vkk")
	time.Sleep(400 * time.Millisecond)
	raw = p.rawVisible()
	if !washed(raw) || !cursored(raw) {
		t.Fatalf("v v k k did not wash rows under a cursor:\n%s", raw)
	}
	t.Logf("after v v k k:\n%s", raw)

	// w walks a word; / lands the cursor on a match and the wash follows it.
	// The question is above the cursor, so /say reaches it and the wash must
	// then cover the question's row.
	p.typeSlowly("w")
	time.Sleep(300 * time.Millisecond)
	p.typeSlowly("/single")
	p.key("Enter")
	time.Sleep(500 * time.Millisecond)
	raw = p.rawVisible()
	if !washed(raw) || !cursored(raw) {
		t.Fatalf("/single dropped the highlight or the cursor:\n%s", raw)
	}
	t.Logf("after w /single Enter:\n%s", raw)

	// `:` preloads the range.
	p.typeSlowly(":")
	time.Sleep(400 * time.Millisecond)
	if vis := p.visible(); !strings.Contains(vis, ":<,>") {
		t.Fatalf("':' with a highlight up did not preload the range:\n%s", vis)
	}
	p.key("Escape")
	time.Sleep(300 * time.Millisecond)

	p.typeSlowly("y")
	time.Sleep(400 * time.Millisecond)
	if vis := p.visible(); !strings.Contains(vis, "yanked") {
		t.Errorf("y in visual mode showed no yank note:\n%s", vis)
	}
	p.typeSlowly("Y")
	time.Sleep(400 * time.Millisecond)
	vis := p.visible()
	coord := regexp.MustCompile(`yanked <\d+\.\d+:\d+-\d+(\.\d+:\d+)?>!`).FindString(vis)
	if coord == "" {
		t.Errorf("Y did not yank a qualified coordinate:\n%s", vis)
	}
	t.Logf("pane after Y (%s):\n%s", coord, vis)

	// Esc leaves the mode: no cursor, no wash.
	p.key("Escape")
	time.Sleep(300 * time.Millisecond)
	if raw := p.rawVisible(); washed(raw) || cursored(raw) {
		t.Errorf("paint survived Esc:\n%s", raw)
	}

	// SUBMIT SEMANTICS. A command sent from the box with a highlight up
	// leaves visual mode; M-Enter also snaps to the live tail. `:0` is a
	// coordinate jump and not a command, so a verb that reaches the runner
	// is used: `:send` into the idle aria, which queues nothing and starts
	// a turn the assertions do not wait for.
	p.typeSlowly("vV") // a line-wise highlight of the cursor's row
	p.typeSlowly(":send -- say the single word SNAPOK")
	p.tmux("send-keys", "M-Enter") // tmux's own spelling of Alt+Enter (measured: one write, ESC LF)
	time.Sleep(600 * time.Millisecond)
	raw = p.rawVisible()
	if washed(raw) || cursored(raw) {
		t.Errorf("M-Enter left visual mode's paint behind:\n%s", raw)
	}
	if !strings.Contains(p.visible(), "live") {
		t.Errorf("M-Enter did not snap to the live tail:\n%s", p.visible())
	}
	t.Logf("after M-Enter:\n%s", raw)
}

// :fork MEANS WHAT `figaro fork` MEANS, with the transcript standing in for
// the stream: mint, prompt, rebind the shell to the branch, and show it as
// its reply lands. `:fork --stay` mints and prompts, stays on the parent, and
// leaves attendance alone. Both are read off the footer, which carries the
// subject's id, and then off `figaro status` in the same pane after the
// session ends, which is the shell's binding as the shell sees it.
func TestSmoke_ForkFromTheBoxAttendsAndShowsUnlessStay(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.send(bin + " send -l -- 'say the single word VISOK and nothing else'")
	p.key("Enter")
	p.waitIdle(120 * time.Second)
	if !p.alive() || !strings.Contains(p.visible(), "VISOK") {
		decline(t, "the first turn did not land with the session still open")
	}
	footerID := regexp.MustCompile(`· ([0-9a-f]{8}) ·`)
	m := footerID.FindStringSubmatch(p.visible())
	if m == nil {
		t.Fatalf("no aria id in the footer:\n%s", p.visible())
	}
	parent := m[1]

	// :fork -- prompt: the footer must show a DIFFERENT id, and the branch's
	// reply must land on it.
	p.typeSlowly(":fork -- say the single word FORKOK and nothing else")
	p.key("Enter")
	deadline := time.Now().Add(90 * time.Second)
	branch := ""
	for time.Now().Before(deadline) {
		vis := p.visible()
		if m := footerID.FindStringSubmatch(vis); m != nil && m[1] != parent {
			branch = m[1]
			if strings.Contains(vis, "FORKOK") {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if branch == "" {
		t.Fatalf(":fork did not show the branch (footer still %s):\n%s", parent, p.visible())
	}
	if !strings.Contains(p.visible(), "FORKOK") {
		t.Errorf("the branch's reply did not land on the shown transcript:\n%s", p.visible())
	}
	t.Logf("showed %s -> %s:\n%s", parent, branch, p.visible())

	// :fork --stay: the footer keeps the branch, and the note names the
	// new one to listen to.
	p.typeSlowly(":fork --stay -- say the single word STAYOK and nothing else")
	p.key("Enter")
	time.Sleep(4 * time.Second)
	vis := p.visible()
	if m := footerID.FindStringSubmatch(vis); m == nil || m[1] != branch {
		t.Errorf(":fork --stay changed the subject:\n%s", vis)
	}
	if !strings.Contains(vis, ":listen") {
		t.Errorf(":fork --stay did not name the branch to listen to:\n%s", vis)
	}
	t.Logf("after :fork --stay:\n%s", vis)

	// THE SHELL'S BINDING, as the shell sees it: leave the session and ask
	// figaro status in the same pane. It must resolve to the branch the
	// first :fork attended, not to the parent and not to --stay's branch.
	// q closes a pit first (the --stay note is one), then the session.
	for i := 0; i < 3 && p.alive(); i++ {
		p.typeSlowly("q")
		time.Sleep(800 * time.Millisecond)
	}
	if p.alive() {
		t.Fatalf("q did not end the session:\n%s", p.visible())
	}
	p.send("clear; " + bin + " status -j")
	p.key("Enter")
	p.waitIdle(30 * time.Second)
	status := p.visible()
	if !strings.Contains(status, `"id": "`+branch+`"`) && !strings.Contains(status, `"id":"`+branch+`"`) {
		t.Errorf("after :fork the shell does not attend the branch %s:\n%s", branch, status)
	}
	t.Logf("figaro status after the session:\n%s", status)
}

// The fork point in the pager: the banner a delta table draws, the f j / f k
// travel to it, `a` attending the aria it names, ^O/^I walking the jumplist,
// and `:ls` + `a` attending a listed aria.
//
// EVERY ASSERTION HERE IS ABOUT A SCREEN OR A BINDING, which is exactly what
// the unit tests cannot see: the banner's placement is decided on the daemon,
// its glyph has to survive the font, and the attend is a real rebinding of a
// real shell. Reproduces the bug it was written for: the fork delta drew on
// the turn BEFORE the fork (aria 5d366cc5).
func TestSmoke_ForkPointJumpAttendAndJumplist(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A SEND DOES NOT OWN ITS CONNECTION, and a session that does not own it
	// may not change subject (command.go). `a` and the jumplist are attends,
	// so the fixture is a send that ENDS and a `listen` that follows it,
	// which is the shape a reader is in when they fork.
	p.send(bin + " send -- 'say the single word ROOTOK and nothing else'")
	p.key("Enter")
	p.waitIdle(120 * time.Second)
	if bodyLines(p.scrollback(), "ROOTOK") == 0 {
		decline(t, "the first turn did not land")
	}
	for i := 0; i < 20 && p.alive(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	p.send("clear; " + bin + " listen")
	p.key("Enter")
	p.waitIdle(30 * time.Second)
	footerID := regexp.MustCompile(`· ([0-9a-f]{8}) ·`)
	m := footerID.FindStringSubmatch(p.visible())
	if m == nil {
		t.Fatalf("no aria id in the footer:\n%s", p.visible())
	}
	parent := m[1]

	// THE PROMPT CONTAINS THE TOKEN, so "is it on screen" is not "has it
	// answered": bodyLines counts only a row that IS the token, which the
	// echoed question never is (trap 2 of the tmux-testing skill).
	p.typeSlowly(":fork -- say the single word BRANCHOK and nothing else")
	p.key("Enter")
	branch := ""
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		vis := p.visible()
		if m := footerID.FindStringSubmatch(vis); m != nil && m[1] != parent {
			branch = m[1]
			if bodyLines(vis, "BRANCHOK") > 0 {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if branch == "" || bodyLines(p.visible(), "BRANCHOK") == 0 {
		decline(t, ":fork did not land the branch's answer")
	}
	// The banner is stamped when the turn SEALS, so give the seal its frame.
	p.waitIdle(30 * time.Second)

	// THE BANNER, AND WHERE IT SITS. It names the parent, and it is drawn
	// with the turn the fork OPENED (the one that answered BRANCHOK), never
	// with the parent's last turn above it.
	vis := p.visible()
	banner := "⑂ " + parent
	if !strings.Contains(vis, banner) {
		t.Fatalf("the fork banner %q is not on screen:\n%s", banner, vis)
	}
	lines := strings.Split(vis, "\n")
	bannerRow, answerRow := -1, -1
	for i, l := range lines {
		if strings.Contains(l, banner) {
			bannerRow = i
		}
		if strings.TrimSpace(l) == "BRANCHOK" && answerRow < 0 {
			answerRow = i
		}
	}
	if bannerRow < 0 || answerRow < 0 || bannerRow > answerRow {
		t.Errorf("the banner must open the forked turn, not close the one before it (banner=%d answer=%d):\n%s",
			bannerRow, answerRow, vis)
	}

	// f j travels to it FROM THE TOP and selects it: the gutter on the
	// banner's own row is the proof, not a gutter anywhere on the screen.
	p.key("g")
	p.key("g")
	time.Sleep(500 * time.Millisecond)
	p.key("f")
	time.Sleep(300 * time.Millisecond)
	p.key("j")
	time.Sleep(time.Second)
	marked := false
	for _, l := range strings.Split(p.rawVisible(), "\n") {
		if strings.Contains(l, parent) && strings.Contains(l, "⑂") && strings.Contains(l, "▎") {
			marked = true
		}
	}
	if !marked {
		t.Errorf("f j did not select the fork point:\n%s", p.visible())
	}

	// `a` attends the aria the fork came from.
	p.key("a")
	time.Sleep(400 * time.Millisecond)
	t.Logf("just after `a`:\n%s", p.visible())
	if !waitFooterID(p, footerID, parent, 30*time.Second) {
		t.Fatalf("`a` on the fork point did not attend %s:\n%s", parent, p.visible())
	}

	// ^O back to the branch, ^I forward to the parent again.
	p.key("C-o")
	if !waitFooterID(p, footerID, branch, 30*time.Second) {
		t.Fatalf("^O did not go back to %s:\n%s", branch, p.visible())
	}
	p.key("Tab")
	if !waitFooterID(p, footerID, parent, 30*time.Second) {
		t.Fatalf("^I did not go forward to %s:\n%s", parent, p.visible())
	}

	// :ls opens the forest as a pit whose rows are arias; ^N selects one and
	// `a` attends it.
	p.typeSlowly(":ls")
	p.key("Enter")
	time.Sleep(3 * time.Second)
	pit := p.visible()
	if !strings.Contains(pit, parent) && !strings.Contains(pit, branch) {
		t.Fatalf(":ls did not list this aria's family:\n%s", pit)
	}
	p.key("C-n")
	time.Sleep(time.Second)
	p.key("a")
	time.Sleep(3 * time.Second)
	if got := footerID.FindStringSubmatch(p.visible()); got == nil {
		t.Fatalf("after :ls + a there is no aria in the footer:\n%s", p.visible())
	} else if got[1] != parent && got[1] != branch {
		t.Errorf("`a` on a :ls row attended %s, which is neither %s nor %s:\n%s",
			got[1], parent, branch, p.visible())
	}
	t.Logf("fork point, jumplist and :ls all landed:\n%s", p.visible())
}

// waitFooterID polls until the footer names the aria, or gives up.
func waitFooterID(p *pane, re *regexp.Regexp, want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(p.visible()); m != nil && m[1] == want {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
