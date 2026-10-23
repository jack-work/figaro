package cli

// FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v -count=1

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// A completed turn returns the shell.
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
	for i := 0; i < 20 && p.alive(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if p.alive() {
		t.Fatalf("figaro is STILL RUNNING after the turn completed: the user cannot exit the view\n%s",
			p.visible())
	}
}

// Every exit key dismisses a streaming turn.
func TestSmoke_ExitKeysWork(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	for _, k := range []string{"C-c", "C-d"} {
		t.Run(k, func(t *testing.T) {
			env, bin := smokeStore(t), smokeBinary(t)
			p := newPane(t, env, bin, 100, 30)
			p.startTurn("use bash to sleep 60, then say SLOWDONE")
			time.Sleep(12 * time.Second)
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

// Letters in the inline view are keybindings, not draft text.
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

	p.typeSlowly("j")
	time.Sleep(time.Second)

	vis := p.visible()
	if strings.Contains(vis, "steer ↳") || strings.Contains(vis, "send ↳") {
		t.Fatalf("a letter opened a text box: the composer is back\n%s", vis)
	}
	if c := pagerChrome(vis); c == 0 {
		t.Fatalf("'j' did not reach the pager: it must scroll, not be swallowed\n%s", vis)
	}
}

// The live view and `fig show` agree about node order. The steer must land
// after a tool has completed or there is nothing to misorder.
func TestSmoke_SteerOrderMatchesShow(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 100)

	p.startTurn("run 3 readonly bash commands with sleep 8 between each, then say ORDEROK")
	time.Sleep(14 * time.Second)
	if !p.alive() {
		decline(t, "turn ended before the steer could land")
	}
	p.typeSlowly("STEERORDER mention plum")
	p.key("Enter")
	p.waitIdle(180 * time.Second)

	sb := p.scrollback()
	if c := pagerChrome(sb); c != 0 {
		// Promotion here is not a function of pane height; a taller pane
		// does not help.
		decline(t, "KNOWN HOLE: view auto-promoted (chrome=%d); the steer path has no pty coverage. "+
			"Do NOT re-run taller -- this pane is already 100x100 and bounding the output was tried too. "+
			"See memory-campaign-open-items.md item 3.", c)
	}
	if got := strings.Count(sb, "↳ input"); got != 1 {
		t.Errorf("steer marker appears %d times, want exactly 1", got)
	}
	lines := strings.Split(sb, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "↳ input") && strings.Contains(lines[i-1], "> input") {
			t.Errorf("steer is hoisted directly under the inquiry header: live order disagrees with fig show\n%s", sb)
			break
		}
	}
}

// Nothing writes to the terminal while the pager owns it. The alternate
// screen has no scrollback, so a stray write lands on the grid at the cursor,
// which the painter parks on the status row; a leading newline there scrolls
// the whole grid under the painter. An invalid key fails the turn with a 401
// before any token, so this costs nothing.
func TestSmoke_ErrorDoesNotBleedIntoStatusBar(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env := append(smokeStore(t), "ANTHROPIC_API_KEY=sk-ant-api03-deliberately-invalid-cherubino")
	bin := smokeBinary(t)
	p := newPane(t, env, bin, 100, 24)

	// -l opens the pager at startup, before the turn can fail.
	p.send(bin + " send -l -- 'say OK'")
	p.key("Enter")
	p.waitIdle(90 * time.Second)

	vis, raw := p.visible(), p.rawVisible()

	if pagerChrome(vis) == 0 {
		return
	}

	// A duplicated status row needs a repaint after the stray write, so it is
	// one-sided and secondary; the escape-sequence check below does not race.
	if got := statusRows(vis); got > 1 {
		t.Errorf("status row appears %d times on the grid, want at most 1: "+
			"a second copy means the grid scrolled under the painter and t.prev "+
			"no longer describes the terminal\n%s", got, vis)
	}
	// Every row the renderer emits carries SGR; an unstyled row bypassed the
	// frame buffer.
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

// A failed inline turn leaves one red bar with the reason, the ✗ and the aria
// id, the shell prompt below it, the question once, and exit status 1.
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
	// Only the bar's alert path colours the reason.
	for _, ln := range strings.Split(raw, "\n") {
		if strings.Contains(ln, "error:") && !strings.Contains(ln, "38;5;167") {
			t.Errorf("the reason is on the bar but not painted red: %q", ln)
		}
	}
	// EXIT= is printed by the shell after figaro exits; anything of ours after
	// it was painted over a returned prompt.
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
	if n := bodyLines(sb, "say OK"); n != 1 {
		t.Errorf("the question appears %d times as a body line, want 1:\n%s", n, sb)
	}
}

// A queued message stays visible until it round-trips, and the bar animates
// in a family distinct from the thinking spinner while it is in transit.
func TestSmoke_QueuedMessageIsHeldUntilItRoundTrips(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A send into an idle aria never queues.
	p.startTurn("use bash to sleep 40, then say FIRSTOK")
	time.Sleep(12 * time.Second)
	if !p.alive() {
		decline(t, "the first turn ended before a message could be queued behind it")
	}

	p.typeSlowly(":send -- QUEUETOK")
	p.key("Enter")

	// The property lives in the interval right after the send, not at turn end.
	deadline := time.Now().Add(20 * time.Second)
	var sawRow, sawTransit string
	for time.Now().Before(deadline) {
		vis := p.visible()
		if sawRow == "" && strings.Contains(vis, "QUEUETOK") {
			sawRow = vis
		}
		if sawTransit == "" && strings.ContainsAny(vis, spinnerGlyphs) {
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
		t.Errorf("no mid-turn indicator appeared between submit and the daemon's "+
			"acknowledgement\n%s", p.visible())
	}

	time.Sleep(2 * time.Second)
	if vis := p.visible(); sawRow != "" && !strings.Contains(vis, "QUEUETOK") {
		t.Errorf("the queued message VANISHED before its turn reached the transcript. "+
			"That gap is the bug: the message is in neither the queue nor the "+
			"conversation, and the reader is left with nothing\n%s", vis)
	}
}

// An open queue drawer follows the queue. The second message comes from
// outside the pane: `:send` into a busy aria already opens the drawer, and
// with it open Enter goes to the pit, so an inside sender tests its own keys.
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

	aria := ariaIDOnScreen(p.visible())
	if aria == "" {
		decline(t, "could not read the aria id off the status row\n%s", p.visible())
	}

	p.typeSlowly(":send -- DRAWERONE")
	p.key("Enter")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(p.visible(), "DRAWERONE") {
		time.Sleep(200 * time.Millisecond)
	}
	if vis := p.visible(); !strings.Contains(vis, "DRAWERONE") {
		decline(t, "the drawer never opened on the first message\n%s", vis)
	}

	figCmd(t, env, bin, "send", "-f", "--id", aria, "--", "DRAWERTWO")

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		vis := p.visible()
		if strings.Contains(vis, "DRAWERTWO") {
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

// footerAriaID reads the aria's name off the status row: its two-word handle,
// or a hex id where the bar has no words for it.
var footerAriaID = regexp.MustCompile(`(?m)(?:^|· )([a-z]+-[a-z]+(?:-[a-z]+)?|[0-9a-f]{5,32}) ·`)

// ariaIDOnScreen takes the first aria name on a status row; a mantra could
// carry one too.
func ariaIDOnScreen(capture string) string {
	re := regexp.MustCompile(`\b[a-z]+-[a-z]+(?:-[a-z]+)?\b|\b[0-9a-f]{5,32}\b`)
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

// Visual mode end to end: cursor, anchored wash across a node boundary, `:`
// range preload, y and Y yanks, read off the raw SGR capture.
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
	// tmux re-encodes SGR per cell and splits combined sequences, so match the
	// background parameter alone, in either spelling.
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

	p.typeSlowly("vkk")
	time.Sleep(400 * time.Millisecond)
	raw = p.rawVisible()
	if !washed(raw) || !cursored(raw) {
		t.Fatalf("v v k k did not wash rows under a cursor:\n%s", raw)
	}
	t.Logf("after v v k k:\n%s", raw)

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

	p.key("Escape")
	time.Sleep(300 * time.Millisecond)
	if raw := p.rawVisible(); washed(raw) || cursored(raw) {
		t.Errorf("paint survived Esc:\n%s", raw)
	}

	// A command submitted with a highlight up leaves visual mode; M-Enter also
	// snaps to the live tail. `:0` is a jump, not a command, so use a verb.
	p.typeSlowly("vV")
	p.typeSlowly(":send -- say the single word SNAPOK")
	p.tmux("send-keys", "M-Enter") // arrives as one write, ESC LF
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

// :fork attends and shows the branch; :fork --stay mints and prompts without
// changing the subject or the shell's binding.
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
	footerID := footerAriaID
	m := footerID.FindStringSubmatch(p.visible())
	if m == nil {
		t.Fatalf("no aria id in the footer:\n%s", p.visible())
	}
	parent := m[1]

	p.typeSlowly(":fork -- say the single word FORKOK and nothing else")
	p.key("Enter")
	deadline := time.Now().Add(90 * time.Second)
	branch := ""
	for time.Now().Before(deadline) {
		vis := p.visible()
		if m := footerID.FindStringSubmatch(vis); m != nil && m[1] != parent {
			branch = m[1]
			// The echoed question contains the token; only a whole row
			// is an answer.
			if bodyLines(vis, "FORKOK") > 0 {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if branch == "" {
		t.Fatalf(":fork did not show the branch (footer still %s):\n%s", parent, p.visible())
	}
	if bodyLines(p.visible(), "FORKOK") == 0 {
		t.Errorf("the branch's reply did not land on the shown transcript:\n%s", p.visible())
	}
	t.Logf("showed %s -> %s:\n%s", parent, branch, p.visible())

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

// The fork banner sits on the child's turn; f j selects it, `a` attends the
// parent, ^O/^I walk the jumplist, and `:ls` + `a` attends a listed aria.
func TestSmoke_ForkPointJumpAttendAndJumplist(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A send does not own its connection and may not change subject, so the
	// fixture is a send that ends and a listen that follows it.
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
	footerID := footerAriaID
	m := footerID.FindStringSubmatch(p.visible())
	if m == nil {
		t.Fatalf("no aria id in the footer:\n%s", p.visible())
	}
	parent := m[1]

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
	// The banner is stamped when the turn seals.
	p.waitIdle(30 * time.Second)

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
	// "Before the answer" alone is also true of a banner on the parent's last turn.
	inquiryRow := -1
	for i, l := range lines {
		if strings.Contains(l, "BRANCHOK") && strings.Contains(l, "say the single word") {
			inquiryRow = i
		}
	}
	if bannerRow < 0 || answerRow < 0 || inquiryRow < 0 || bannerRow < inquiryRow || bannerRow > answerRow {
		t.Errorf("the banner must sit between the child's question and its answer (inquiry=%d banner=%d answer=%d):\n%s",
			inquiryRow, bannerRow, answerRow, vis)
	}

	// The gutter on the banner's own row is the proof, not a gutter anywhere.
	p.key("g")
	p.key("g")
	time.Sleep(500 * time.Millisecond)
	p.key("f")
	time.Sleep(300 * time.Millisecond)
	p.key("j")
	time.Sleep(time.Second)
	marked := false
	raw := p.rawVisible()
	lines, wash := strings.Split(raw, "\n"), capturedWashes(raw)
	for i, l := range lines {
		if strings.Contains(l, parent) && strings.Contains(l, "⑂") && i < len(wash) && wash[i] {
			marked = true
		}
	}
	if !marked {
		t.Errorf("f j did not select the fork point:\n%s", p.visible())
	}

	p.key("a")
	time.Sleep(400 * time.Millisecond)
	t.Logf("just after `a`:\n%s", p.visible())
	if !waitFooterID(p, footerID, parent, 30*time.Second) {
		t.Fatalf("`a` on the fork point did not attend %s:\n%s", parent, p.visible())
	}

	p.key("C-o")
	if !waitFooterID(p, footerID, branch, 30*time.Second) {
		t.Fatalf("^O did not go back to %s:\n%s", branch, p.visible())
	}
	p.key("Tab")
	if !waitFooterID(p, footerID, parent, 30*time.Second) {
		t.Fatalf("^I did not go forward to %s:\n%s", parent, p.visible())
	}

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
