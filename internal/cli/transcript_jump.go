package cli

import (
	"errors"

	"fmt"
	"github.com/jack-work/figaro/api/quote"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Not an aria.Anchor: Anchor{Turn: 0} means unset on the wire, and `:0` means the
// beginning; a zero anchor handed to a backward read returns the tail.
type jumpTarget struct {
	start   bool // ":0": the lowest turn that exists
	turn    int
	node    int
	hasNode bool
	// travel is -1 for the previous turn, +1 for the next.
	travel int
}

func (tg jumpTarget) String() string {
	if tg.travel > 0 {
		return "the next question"
	}
	if tg.travel < 0 {
		return "the previous question"
	}
	if tg.start {
		return "the beginning"
	}
	if tg.hasNode {
		return "turn " + strconv.Itoa(tg.turn) + ", node " + strconv.Itoa(tg.node)
	}
	return "turn " + strconv.Itoa(tg.turn)
}

// The beginning is the zero anchor, which a forward read answers with the head.
func (tg jumpTarget) anchor() aria.Anchor {
	if tg.start {
		return aria.Anchor{}
	}
	if tg.travel > 0 {
		return aria.Anchor{Turn: uint64(tg.turn + 1)}
	}
	if tg.travel < 0 {
		// A backward read excludes its anchor.
		return aria.Anchor{Turn: uint64(tg.turn)}
	}
	a := aria.Anchor{Turn: uint64(tg.turn)}
	if tg.hasNode && tg.node > 0 {
		a.Node = uint64(tg.node)
	}
	return a
}

func (tg jumpTarget) missing() string {
	if tg.start {
		return "nothing here to jump to"
	}
	return "no " + tg.String() + " in this aria"
}

// Accepts `<turn>` and `<turn>.<node>` (node may be the inquiry sentinel -1); a
// coordinate is not an expression.
func parseJumpTarget(s string) (jumpTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return jumpTarget{}, fmt.Errorf("nowhere to go: type :<turn> or :<turn>.<node>")
	}
	turnText, nodeText, hasNode := strings.Cut(s, ".")
	turn, err := strconv.Atoi(strings.TrimSpace(turnText))
	if err != nil || turn < 0 {
		return jumpTarget{}, fmt.Errorf("%q is not a turn", turnText)
	}
	if turn == 0 {
		// Turn 0 is never an address, so ":0.n" is named as a mistake.
		if hasNode {
			return jumpTarget{}, fmt.Errorf("turn 0 is not an address; :0 alone means the beginning")
		}
		return jumpTarget{start: true}, nil
	}
	if !hasNode {
		return jumpTarget{turn: turn}, nil
	}
	node, err := strconv.Atoi(strings.TrimSpace(nodeText))
	if err != nil || node < inquiryNode {
		return jumpTarget{}, fmt.Errorf("%q is not a node", nodeText)
	}
	return jumpTarget{turn: turn, node: node, hasNode: true}, nil
}

type jumpReach uint8

const (
	jumpHere   jumpReach = iota // loaded: land on it
	jumpRead                    // fetch the target page
	jumpAbsent                  // cannot exist
)

type transcriptJump struct {
	target jumpTarget
	asked  *aria.Anchor
	offset int
	follow bool
}

func pagerCommandBox(t *transcript) {
	t.box, t.jumpNote = boxCommand, ""
	t.cmdline.reset()
	t.menu = nil
}

func boxCancel(t *transcript) {
	// Esc takes back the most recent thing first: history search, then the
	// menu, then the box.
	if t.editor().searching() {
		t.editor().endSearch()
		return
	}
	if t.menu != nil {
		t.dismissCompletion()
		return
	}
	// The drawer has one more rung: insert drops into normal mode, where Esc
	// puts it away.
	if t.box == boxComposer {
		composerNormalEnter(t)
		return
	}
	boxClose(t)
}

// A command line goes with the box; a draft stays (cmdAbort spends it).
func boxClose(t *transcript) {
	if t.box == boxComposer {
		t.composerClose(false)
		return
	}
	t.box = boxNone
	t.cmdline.reset()
	t.clearCompletions()
	// A command line opened from the drawer goes back to it.
	t.composerResume()
}

func boxBackspace(t *transcript) {
	// During a history search Backspace shortens the needle.
	if t.editor().searching() {
		t.editor().searchBackspace()
		return
	}
	if t.menu != nil {
		t.editor().endSearch()
		t.editor().backspace()
		t.refilterCompletion()
		return
	}
	t.edit(func(e *lineEditor) { e.backspace() })
}

// edit ends a history search (readline: a key outside the search accepts the line,
// then acts) and drops the completion list.
func (t *transcript) edit(fn func(*lineEditor)) {
	e := t.editor()
	e.endSearch()
	fn(e)
	t.clearCompletions()
}

func cmdHome(t *transcript)      { t.edit(func(e *lineEditor) { e.home() }) }
func cmdEnd(t *transcript)       { t.edit(func(e *lineEditor) { e.end() }) }
func cmdLeft(t *transcript)      { t.edit(func(e *lineEditor) { e.left() }) }
func cmdRight(t *transcript)     { t.edit(func(e *lineEditor) { e.right() }) }
func cmdWordLeft(t *transcript)  { t.edit(func(e *lineEditor) { e.wordLeft() }) }
func cmdWordRight(t *transcript) { t.edit(func(e *lineEditor) { e.wordRight() }) }
func cmdKillToEnd(t *transcript) { t.edit(func(e *lineEditor) { e.killToEnd() }) }
func cmdKillToStart(t *transcript) {
	t.edit(func(e *lineEditor) { e.killToStart() })
}
func cmdKillWord(t *transcript)      { t.edit(func(e *lineEditor) { e.killWordBack() }) }
func cmdKillWordAlpha(t *transcript) { t.edit(func(e *lineEditor) { e.killWordBackAlpha() }) }
func cmdKillWordFwd(t *transcript)   { t.edit(func(e *lineEditor) { e.killWordForward() }) }
func cmdDeleteSpace(t *transcript)   { t.edit(func(e *lineEditor) { e.deleteHorizontalSpace() }) }
func cmdYank(t *transcript)          { t.edit(func(e *lineEditor) { e.yank() }) }
func cmdTranspose(t *transcript)     { t.edit(func(e *lineEditor) { e.transposeChars() }) }
func cmdTransposeWord(t *transcript) { t.edit(func(e *lineEditor) { e.transposeWords() }) }
func cmdUpcase(t *transcript)        { t.edit(func(e *lineEditor) { e.caseWord('u') }) }
func cmdDowncase(t *transcript)      { t.edit(func(e *lineEditor) { e.caseWord('l') }) }
func cmdCapitalize(t *transcript)    { t.edit(func(e *lineEditor) { e.caseWord('c') }) }
func cmdRevert(t *transcript)        { t.edit(func(e *lineEditor) { e.revert() }) }
func cmdHistFirst(t *transcript)     { t.edit(func(e *lineEditor) { e.historyFirst() }) }
func cmdHistLast(t *transcript)      { t.edit(func(e *lineEditor) { e.historyLast() }) }
func cmdPrefixPrev(t *transcript)    { t.edit(func(e *lineEditor) { e.historyPrefix(-1) }) }
func cmdPrefixNext(t *transcript)    { t.edit(func(e *lineEditor) { e.historyPrefix(1) }) }
func cmdYankLastArg(t *transcript)   { t.edit(func(e *lineEditor) { e.yankLastArg() }) }

// Not through edit(): only the editor knows whether the last action was a yank.
func cmdYankPop(t *transcript) {
	if !t.editor().yankPop() {
		return
	}
	t.clearCompletions()
}

// Says so when nothing is left: a silent key is indistinguishable from an unbound one.
func cmdUndo(t *transcript) {
	t.editor().endSearch()
	t.clearCompletions()
	if !t.editor().undoOne() {
		t.noteOrClear("nothing to undo")
	}
}

// bash's ^D: delete under the cursor, or close the box when the line is empty; the
// next ^D detaches.
func cmdDeleteFwd(t *transcript) {
	if t.editor().searching() {
		t.editor().endSearch()
		return
	}
	if t.editor().empty() {
		boxClose(t)
		return
	}
	t.edit(func(e *lineEditor) { e.deleteForward() })
}

// readline's ^G: inside a search it restores the line the search started from.
func cmdAbort(t *transcript) {
	if t.editor().searching() {
		t.editor().abortSearch()
		return
	}
	// ^C and ^G spend the draft; Esc only puts the drawer away.
	if t.box == boxComposer {
		t.composerClose(true)
		return
	}
	boxClose(t)
}

// Repaints from nothing: the repair for a screen another program scribbled on.
func cmdRedraw(t *transcript) {
	t.prev = nil
	t.render()
}

func cmdSearchPrev(t *transcript) {
	t.clearCompletions()
	t.editor().searchAgain(-1)
}

// In the composer ^S submits: Alt+Enter never arrives on Windows (conhost and
// Windows Terminal claim it for fullscreen). Safe in raw mode: IXON is off, so the
// byte arrives instead of pausing the tty.
func cmdSearchNext(t *transcript) {
	if t.box == boxComposer {
		composerSubmit(t)
		return
	}
	t.clearCompletions()
	t.editor().searchAgain(1)
}

// ^P/^N move the menu while it is up, and walk history when it is not.
func cmdHistPrev(t *transcript) {
	if t.menu != nil {
		t.cycleCompletion(-1)
		return
	}
	// In a draft they are line motions.
	if t.box == boxComposer {
		t.edit(func(e *lineEditor) { e.lineUp() })
		return
	}
	t.edit(func(e *lineEditor) { e.historyPrev() })
}

func cmdHistNext(t *transcript) {
	if t.menu != nil {
		t.cycleCompletion(1)
		return
	}
	if t.box == boxComposer {
		t.edit(func(e *lineEditor) { e.lineDown() })
		return
	}
	t.edit(func(e *lineEditor) { e.historyNext() })
}

// A printable key is text, not a binding; during a history search the same bytes
// grow the needle instead (mirrors searchLiteral).
func (t *transcript) boxLiteral(b byte) {
	e := t.editor()
	if e.searching() {
		if r, ok := searchRune(b); ok {
			e.searchType(r)
		}
		return
	}
	if !e.insertByte(b) {
		return
	}
	// Typing narrows the open menu; a space finishes the word and the menu.
	if t.menu != nil && b != ' ' {
		t.refilterCompletion()
		return
	}
	t.clearCompletions()
}

// A needle built one byte at a time from a multi-byte rune would search for half a
// character.
func searchRune(b byte) (rune, bool) {
	if b < 0x20 || b >= 0x7f {
		return 0, false
	}
	return rune(b), true
}

// A bare coordinate is handled here; anything else goes to the owner of the
// transcript, which holds the RPC client.
func boxAccept(t *transcript) { boxEnter(t) }

// Submit, then leave visual mode and re-follow the live tail.
func boxAcceptSnap(t *transcript) {
	if t.box == boxComposer {
		composerSubmit(t)
		return
	}
	t.boxSubmit(true)
}

// Submitting leaves visual mode and keeps the place: viewport and last search
// survive. A coordinate jump leaves the mode alone.
func (t *transcript) boxSubmit(snap bool) {
	t.cmdline.endSearch()
	// Enter on a chosen candidate takes it and leaves the line to be finished.
	if t.acceptCompletion() {
		return
	}
	text := strings.TrimSpace(t.cmdline.String())
	t.cmdline.remember(text)
	t.cmdline.reset()
	t.box, t.jumpNote = boxNone, ""
	t.composerResume()
	t.menu = nil
	if text == "" {
		return
	}
	if tg, err := parseJumpTarget(text); err == nil {
		t.startJump(tg)
		return
	}
	if t.command == nil {
		t.noteOrClear("commands need a live session")
		return
	}
	// The range is expanded here, under the render lock: the coordinate is
	// read off the index and the selection, which the next keystroke may move.
	expanded, note, err := t.expandRange(text)
	if err != "" {
		t.noteOrClear(err)
		return
	}
	if note != "" {
		t.setCommandNoteAt(note, alertInfo)
	} else {
		t.jumpNote = "" // the runner owns the row from here; see setCommandNote
	}
	t.leaveVisual()
	if snap {
		pagerTail(t)
	}
	t.command(expanded)
}

// expandRange rewrites `<,>verb ... -- text` into `verb ... -- <lt.block:a-b>!
// text`; a qualified token typed by hand is moved the same way.
func (t *transcript) expandRange(text string) (string, string, string) {
	var token, note string
	rest := text
	switch {
	case strings.HasPrefix(text, visualRangePlaceholder):
		if !t.visual.highlighted() {
			return "", "", "no highlight: the range was dropped, or never made (v / V at the cursor)"
		}
		tok, n, err := t.visualCoordinate()
		if err != "" {
			return "", "", "range: " + err
		}
		token, note = tok, n
		rest = strings.TrimSpace(text[len(visualRangePlaceholder):])
	case strings.HasPrefix(text, "<"):
		r, after, err := quote.Parse(text)
		if err != nil {
			if errors.Is(err, quote.ErrNone) {
				return text, "", ""
			}
			return "", "", "range: " + err.Error()
		}
		token, rest = quote.Format(r), strings.TrimSpace(after)
	default:
		return text, "", ""
	}
	if rest == "" {
		return "", "", "range: a verb must follow it (send, fork)"
	}
	// The prompt is the reader's bytes: the token is spliced in front, never
	// re-joined through Fields, which collapsed quoted spaces.
	head, tail, ok := cutVerbBoundary(rest)
	if !ok {
		return "", "", "range: the prompt must follow `--` (" + strings.Fields(rest)[0] + " -- <text>)"
	}
	return head + " " + token + tail, note, ""
}

// The tail keeps its leading space.
func cutVerbBoundary(line string) (head, tail string, ok bool) {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '-' || line[i+1] != '-' {
			continue
		}
		if i > 0 && line[i-1] != ' ' && line[i-1] != '\t' {
			continue
		}
		if end := i + 2; end == len(line) || line[end] == ' ' || line[end] == '\t' {
			return strings.TrimSpace(line[:end]), line[end:], true
		}
	}
	return "", "", false
}

func (t *transcript) startJump(tg jumpTarget) {
	t.search = nil
	t.abandonJump("")
	t.settle()
	line, ref, reach := t.jumpReachOf(tg)
	if reach == jumpHere {
		t.landJump(line, ref)
		return
	}
	if reach == jumpAbsent {
		t.noteOrClear(tg.missing())
		return
	}
	t.jump = &transcriptJump{target: tg, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

func (t *transcript) jumpAdvance() {
	if t.jump == nil {
		return
	}
	line, ref, reach := t.jumpReachOf(t.jump.target)
	if reach == jumpHere {
		t.jump = nil
		t.landJump(line, ref)
	} else if reach == jumpAbsent {
		t.abandonJump(t.jump.target.missing())
	}
}

func (t *transcript) jumpSeek() (aria.Anchor, int, bool) {
	if t.jump == nil || t.jump.asked != nil {
		return aria.Anchor{}, 0, false
	}
	at, dir := t.jump.target.anchor(), seekForward
	if t.jump.target.travel < 0 {
		dir = seekBackward
	}
	t.jump.asked = &at
	return at, dir, true
}

func (t *transcript) jumpReachOf(tg jumpTarget) (int, nodeRef, jumpReach) {
	if tg.travel != 0 {
		tg.turn += tg.travel
		tg.travel = 0
		if tg.turn < 1 {
			return 0, nodeRef{}, jumpAbsent
		}
	}
	if tg.start {
		if !t.atAriaFloor() || len(t.index.entries) == 0 || t.index.entries[0].isGap() {
			return 0, nodeRef{}, jumpRead
		}
		tg.turn = t.index.entries[0].turn
	}
	if tg.hasNode {
		ref := nodeRef{turn: tg.turn, index: tg.node}
		if span, ok := t.nodeSpanOf(ref); ok {
			return span.first, ref, jumpHere
		}
	} else {
		for k := range t.index.entries {
			e := &t.index.entries[k]
			if !e.isGap() && e.turn == tg.turn && e.key.from() == 0 {
				return entryRowsStart(e), t.firstRefOfTurn(tg.turn), jumpHere
			}
		}
	}
	return 0, nodeRef{}, jumpRead
}

func (t *transcript) firstRefOfTurn(turn int) nodeRef {
	ref := nodeRef{}
	take := func(m aria.Message) bool {
		if m.Inquiry != "" {
			ref = nodeRef{turn: turn, index: inquiryNode}
		} else if len(m.Nodes) > 0 {
			ref = nodeRefAt(m, 0)
		}
		return !ref.valid()
	}
	t.client.ForEachIn(aria.Anchor{Turn: uint64(turn)}, aria.Anchor{Turn: uint64(turn), Node: ^uint64(0)}, take)
	if m := t.openMessage(); !ref.valid() && m != nil && m.Turn == turn {
		take(*m)
	}
	return ref
}

func (t *transcript) landJump(line int, ref nodeRef) {
	t.wantTop = false
	t.jumpNote = ""
	t.stopFollowing()
	if ref.valid() {
		t.selectRef(ref, false)
	}
	if line < 0 {
		line = 0
	}
	t.offset = line
}

func (t *transcript) selectRef(ref nodeRef, extend bool) bool {
	t.wantTop = false
	var point selectionPoint
	found := false
	// A delta row's hash is the row's own, so the copier's guard catches state
	// that moved.
	takeDelta := func(block nodeRef, deltas map[string]livedoc.FormDelta) {
		for _, p := range deltaPoints(block, deltas, true) {
			if p.nodeRef == ref {
				point, found = p, true
				return
			}
		}
	}
	take := func(m aria.Message) bool {
		switch {
		case ref.index == inquiryNode:
			if ref.delta > 0 {
				takeDelta(nodeRef{turn: m.Turn, index: inquiryNode}, m.FormDeltas)
			} else {
				point, found = inquiryPoint(m)
			}
		default:
			if i := ref.index - int(m.From); i >= 0 && i < len(m.Nodes) {
				if ref.delta > 0 {
					takeDelta(nodeRefAt(m, i), m.Nodes[i].FormDeltas)
				} else {
					point, found = selectionPoint{nodeRef: ref, hash: nodeHash(m.Nodes[i])}, true
				}
			}
		}
		return !found
	}
	at := aria.Anchor{Turn: uint64(ref.turn), Node: uint64(max(ref.index, 0))}
	t.client.ForEachIn(at, at, take)
	if m := t.openMessage(); !found && m != nil && m.Turn == ref.turn {
		take(*m)
	}
	if !found {
		return false
	}
	if !extend || !t.selection.active {
		t.selection.anchor = point
	}
	t.selection.focus = point
	t.selection.active = true
	return true
}

// An empty note clears the walk silently.
func (t *transcript) abandonJump(note string) {
	if t.jump == nil {
		t.noteOrClear(note)
		return
	}
	origin := t.jump
	t.jump = nil
	t.offset = origin.offset
	t.follow = origin.follow
	t.noteOrClear(note)
	t.pruneCaches()
}

func (t *transcript) noteOrClear(note string) {
	t.jumpNote = note
	if note == "" {
		if t.showing("message") {
			t.pit.close()
		}
		return
	}
	t.pit.showNote(note)
}

func (t *transcript) jumpFooter() (string, bool) {
	if t.jump != nil {
		return "jumping to " + t.jump.target.String() + "…", true
	}
	return "", false
}

// ^V: the system clipboard, folded onto one line in a command box.
func cmdPaste(t *transcript) {
	text, err := clipboardRead()
	if err != nil {
		t.noteOrClear("paste: " + err.Error())
		return
	}
	// A draft keeps the lines.
	if t.box == boxComposer {
		t.editor().endSearch()
		t.clearCompletions()
		t.editor().insertPasted(text, true)
		return
	}
	if text = pasteIntoLine(text); text == "" {
		return
	}
	t.edit(func(e *lineEditor) { e.insert(text) })
}

func (t *transcript) jumpTurn(dir int) {
	t.settle()
	turn := 0
	if k := t.index.entryAt(t.offset); k >= 0 && !t.index.entries[k].isGap() {
		turn = t.index.entries[k].turn
	}
	if t.selection.active && t.selection.focus.index == inquiryNode {
		if span, ok := t.nodeSpanOf(t.selection.focus.nodeRef); ok {
			top, bottom := t.viewportLines()
			if span.last >= top && span.first < bottom {
				turn = t.selection.focus.turn
			}
		}
	}
	if turn > 0 {
		t.startJump(jumpTarget{turn: turn, travel: dir})
	}
}
