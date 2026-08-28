package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// THE `:` JUMP: go to a coordinate.

// jumpBudget is how many page fetches one jump may spend before giving up.
const jumpBudget = 24

// jumpTarget is a parsed coordinate. It is deliberately NOT an aria.Anchor:
// Anchor{Turn: 0} means UNSET on the wire, and `:0` means the opposite of
// unset. Keeping the sentinel in the parser's own type is what stops a
// backward read from being handed a zero anchor and returning the tail.
type jumpTarget struct {
	start   bool // ":0": the lowest turn that exists
	turn    int
	node    int
	hasNode bool
}

func (tg jumpTarget) String() string {
	if tg.start {
		return "the beginning"
	}
	if tg.hasNode {
		return "turn " + strconv.Itoa(tg.turn) + ", node " + strconv.Itoa(tg.node)
	}
	return "turn " + strconv.Itoa(tg.turn)
}

// missing is what the footer says when the target cannot exist.
func (tg jumpTarget) missing() string {
	if tg.start {
		return "nothing here to jump to"
	}
	return "no " + tg.String() + " in this aria"
}

// parseJumpTarget reads the text of the jump box. It accepts `<turn>` and
// `<turn>.<node>`, with the node allowed to be the inquiry sentinel (-1), and
// nothing else, a coordinate is not an expression.
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
		// Turn 0 is the reserved sentinel; it is never an address, so ":0.n"
		// is a mistake worth naming rather than silently reading as ":0".
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

// jumpReach is what the retained window can do about a target right now.
type jumpReach uint8

const (
	jumpHere   jumpReach = iota // it is loaded: land on it
	jumpOlder                   // page backwards toward it
	jumpAbsent                  // it cannot exist; say so
)

// transcriptJump is a walk in progress: the target, what is left of the
// budget, and enough of the origin to put the reader back where they were if
// the walk fails. It mirrors transcriptSearch, which is the same shape for the
// same reason, a paged search and a paged jump are one traversal with two
// stopping conditions. As there, only the VIEWPORT is restored: history the
// walk paged in is in the store, and throwing the floor back up would only
// re-fetch it.
type transcriptJump struct {
	target  jumpTarget
	fetches int

	offset int
	follow bool
}

// ---------------------------------------------------------------------------
// The keymap actions. One row each; see keymap.go.
// ---------------------------------------------------------------------------

// pagerJumpPrompt is ':': open the command line.
func pagerJumpPrompt(t *transcript) {
	t.inJump, t.jumpNote = true, ""
	t.cmdline.reset()
	t.completions = nil
}

func jumpCancel(t *transcript) {
	// ESC IS THE LADDER OUT, one rung per press, and it takes back the most
	// recent thing first: a running history search, then the completion menu's
	// offer, then the box itself. That is what a shell does, and it is why the
	// same key can be "toggle command mode off" without ever discarding a line
	// you were still looking at.
	if t.cmdline.searching() {
		t.cmdline.endSearch()
		return
	}
	if len(t.completions) > 0 {
		t.clearCompletions()
		return
	}
	jumpClose(t)
}

// jumpClose puts the box away. The line goes with it -- ^C at a shell prompt
// does not leave its text lying around either.
func jumpClose(t *transcript) {
	t.inJump = false
	t.cmdline.reset()
	t.clearCompletions()
}

func jumpBackspace(t *transcript) {
	// While a history search runs, Backspace shortens the NEEDLE: that is what
	// it does at a shell, and it is the only way to back out of a failed
	// search without abandoning it.
	if t.cmdline.searching() {
		t.cmdline.searchBackspace()
		return
	}
	t.edit(func(e *lineEditor) { e.backspace() })
}

// edit runs one editing motion and drops the completion list: any change to
// the line makes the last Tab's candidates a lie.
//
// IT ALSO ENDS A HISTORY SEARCH, keeping what the search found. readline's
// rule: any key that is not part of the search accepts the line it landed on
// and then acts. Putting it here rather than in each action is what makes that
// true of every binding, including ones added later.
func (t *transcript) edit(fn func(*lineEditor)) {
	t.cmdline.endSearch()
	fn(&t.cmdline)
	t.clearCompletions()
}

// The emacs/readline motions, one row each in the keymap. Names are readline's
// because the fingers pressing these keys learned them there.
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

// cmdYankPop is M-y. It must NOT go through edit(): edit ends a search and
// clears completions, neither of which matters here, but it would also be the
// wrong place to hide the one rule this key has -- it only follows a yank, and
// the editor is what knows whether the last thing that happened was one.
func cmdYankPop(t *transcript) {
	if !t.cmdline.yankPop() {
		return
	}
	t.clearCompletions()
}

// cmdUndo is ^_ : one step back, and it says so when there is nothing left,
// because a key that silently does nothing is indistinguishable from a key
// that is not bound.
func cmdUndo(t *transcript) {
	t.cmdline.endSearch()
	t.clearCompletions()
	if !t.cmdline.undoOne() {
		t.noteOrClear("nothing to undo")
	}
}

// cmdDeleteFwd is ^D, and it carries the ONE rule that made unbinding detach
// inside this box safe: on an EMPTY line it closes the box instead of deleting
// nothing. That is bash's ^D exactly -- delete the character under the cursor,
// or leave when there is no line -- and it keeps the escape hatch one press
// deeper rather than removing it: ^D on an empty box closes it, and the next
// ^D detaches the session as it always did.
func cmdDeleteFwd(t *transcript) {
	if t.cmdline.searching() {
		t.cmdline.endSearch()
		return
	}
	if t.cmdline.empty() {
		jumpClose(t)
		return
	}
	t.edit(func(e *lineEditor) { e.deleteForward() })
}

// cmdAbort is ^G, and ^C: abandon the line, keep the pager. readline's ^G.
// Inside a search it puts back the line the search was started from, which is
// the only way to say "no, never mind" to a search that has walked far away.
func cmdAbort(t *transcript) {
	if t.cmdline.searching() {
		t.cmdline.abortSearch()
		return
	}
	jumpClose(t)
}

// cmdRedraw is ^L: readline's clear-screen. The pager owns the whole grid, so
// clearing it means painting it again from nothing -- which is also the repair
// for a screen some other program has scribbled on.
func cmdRedraw(t *transcript) {
	t.prev = nil
	t.render()
}

// cmdSearchPrev / cmdSearchNext are ^R / ^S: the incremental history search.
func cmdSearchPrev(t *transcript) {
	t.clearCompletions()
	t.cmdline.searchAgain(-1)
}

func cmdSearchNext(t *transcript) {
	t.clearCompletions()
	t.cmdline.searchAgain(1)
}

// ^P/^N are HISTORY when there is no completion menu and MENU MOVEMENT when
// there is -- which is what a shell does, and what Gluck asked for: the menu
// takes the keys while it is up and the history has them back the moment it is
// not.
func cmdHistPrev(t *transcript) {
	if len(t.completions) > 0 {
		t.cycleCompletion(-1)
		return
	}
	t.edit(func(e *lineEditor) { e.historyPrev() })
}

func cmdHistNext(t *transcript) {
	if len(t.completions) > 0 {
		t.cycleCompletion(1)
		return
	}
	t.edit(func(e *lineEditor) { e.historyNext() })
}

// cmdListComplete is M-?: show the candidates and insert nothing. Tab is the
// key that commits; this is the one you press when you only want to look.
func cmdListComplete(t *transcript) {
	if t.completer == nil {
		return
	}
	t.cmdline.endSearch()
	cands := t.completer(t.cmdline.String())
	t.clearCompletions()
	if len(cands) == 0 {
		t.noteOrClear("no completions")
		return
	}
	t.completionAt = t.cmdline.cursor - len([]rune(lastWord(t.cmdline.String())))
	t.completions, t.completionIdx = cands, -1
}

// cmdInsertComplete is M-*: put every candidate in the line, space-separated.
// Rare, and cheap to have: it is how you turn "which arias are there" into a
// line you then edit down.
func cmdInsertComplete(t *transcript) {
	if t.completer == nil {
		return
	}
	t.cmdline.endSearch()
	cands := t.completer(t.cmdline.String())
	if len(cands) == 0 {
		return
	}
	word := lastWord(t.cmdline.String())
	t.clearCompletions()
	for range []rune(word) {
		t.cmdline.backspace()
	}
	t.cmdline.insert(strings.Join(cands, " ") + " ")
}

// cmdComplete is Tab. The candidates come from the ROUTER's own completion --
// the same __complete verb the shell scripts call -- so aria ids, form ids and
// flags are completed here by the code that already knew how.
func cmdComplete(t *transcript) {
	// A SECOND TAB CYCLES, it does not recompute: that is what makes Tab Tab
	// Tab walk the list, as it does in bash's menu-complete and in fish.
	if len(t.completions) > 0 {
		t.cycleCompletion(1)
		return
	}
	if t.completer == nil {
		return
	}
	line := t.cmdline.String()
	cands := t.completer(line)
	t.clearCompletions()
	if len(cands) == 0 {
		return
	}
	word := lastWord(line)
	t.completionAt = t.cmdline.cursor - len([]rune(word))
	// FIRST TAB INSERTS THE LONGEST THING THAT CANNOT BE WRONG, which is what
	// both shells do before they offer to show you anything.
	if pre := commonPrefix(cands); len(pre) > len(word) {
		t.cmdline.insert(pre[len(word):])
	}
	if len(cands) == 1 {
		t.cmdline.insert(" ") // an unambiguous completion moves you along
		return
	}
	t.completions, t.completionIdx = cands, -1
}

// lastWord is the partial token under the cursor, for measuring what a
// completion still has to add.
func lastWord(line string) string {
	if line == "" || strings.HasSuffix(line, " ") {
		return ""
	}
	if i := strings.LastIndexAny(line, " \t"); i >= 0 {
		return line[i+1:]
	}
	return line
}

// jumpLiteral is the box's fallback: a printable key is text, not a binding -
// which is what makes '/' an ordinary character in here, as ':' is in the
// search box. The keymap does not enumerate "every printable byte"; see
// searchLiteral, whose contract this mirrors exactly.
//
// While a history search runs the same bytes grow the NEEDLE instead. One
// acceptor, two destinations: that is the whole of what ^R changes about
// typing, and it is why ^R needs no mode of the pager's own.
func (t *transcript) jumpLiteral(b byte) {
	if t.cmdline.searching() {
		if r, ok := searchRune(b); ok {
			t.cmdline.searchType(r)
		}
		return
	}
	if t.cmdline.insertByte(b) {
		t.clearCompletions()
	}
}

// searchRune keeps the needle ASCII-simple: the editor's UTF-8 reassembly
// belongs to the line, and a needle built one byte at a time out of a
// multi-byte rune would search for half a character.
func searchRune(b byte) (rune, bool) {
	if b < 0x20 || b >= 0x7f {
		return 0, false
	}
	return rune(b), true
}

// jumpAccept is Enter in the ':' box, which is a COMMAND LINE with a
// coordinate shorthand -- exactly the arrangement vim has, where `:12` goes to
// line 12 and `:w` writes. A bare coordinate is handled here, because it needs
// nothing but the window; anything else is handed to the owner of this
// transcript, which is the only thing in the process holding an RPC client.
//
// The transcript therefore knows nothing about the command language. It knows
// "this is not a coordinate" and who to give it to.
func jumpAccept(t *transcript) {
	// Enter DURING a search accepts what the search found, and runs it: the
	// line on screen is the line, and a reader who has found it and pressed
	// Enter has said so.
	t.cmdline.endSearch()
	text := strings.TrimSpace(t.cmdline.String())
	t.cmdline.remember(text)
	t.cmdline.reset()
	t.inJump, t.jumpNote = false, ""
	t.completions = nil
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
	t.jumpNote = "" // the runner owns the row from here; see setCommandNote
	t.command(text)
}

// ---------------------------------------------------------------------------
// The walk.
// ---------------------------------------------------------------------------

// startJump resolves a target against the loaded window and, if it is not
// there, arms the existing paging path to walk toward it.
func (t *transcript) startJump(tg jumpTarget) {
	// A jump supersedes a running history search: they are the same traversal
	// and cannot both own the page cursor. Restoring the search's origin first
	// would fight the jump for the viewport, so the search is simply dropped.
	t.search = nil
	t.abandonJump("") // a second ':' replaces the first walk, it does not queue
	t.settle()        // resolve against the CONVERGED window, as find() does

	line, ref, reach := t.jumpReachOf(tg)
	switch reach {
	case jumpHere:
		t.landJump(line, ref)
		return
	case jumpAbsent:
		t.noteOrClear(tg.missing())
		return
	}
	t.jump = &transcriptJump{
		target: tg, fetches: jumpBudget,
		offset: t.offset, follow: t.follow,
	}
	t.stopFollowing()
}

// jumpAdvance is the whole state machine, called after anything that can
// change what the window holds: a page landing, the window growing over
// history the store already had, the floor being proven. It resolves the
// target, or leaves the walk standing so pageCursor spends one more of the
// budget on it.
func (t *transcript) jumpAdvance() {
	if t.jump == nil {
		return
	}
	line, ref, reach := t.jumpReachOf(t.jump.target)
	switch reach {
	case jumpHere:
		t.jump = nil
		t.landJump(line, ref)
	case jumpAbsent:
		t.abandonJump(t.jump.target.missing())
	}
}

// jumpReachOf resolves a target against the loaded window: the absolute line
// to land on, the node to put the selection on, and what to do if neither.
// It reads the line index, so the caller must have built it (settle does).
func (t *transcript) jumpReachOf(tg jumpTarget) (int, nodeRef, jumpReach) {
	entries := t.index.entries
	if len(entries) == 0 {
		if t.atAriaFloor() {
			return 0, nodeRef{}, jumpAbsent
		}
		return 0, nodeRef{}, jumpOlder
	}
	oldest, newest, haveTurns := t.windowTurnBounds()
	if !haveTurns {
		// Nothing but holes in the window: there is no address to compare
		// against yet, and the fill is already owed.
		return 0, nodeRef{}, jumpOlder
	}
	if tg.start {
		// The floor is only known once the store has said so, because nothing
		// on the wire says where an aria begins, an empty ReadBefore is the
		// only proof there is. So this lands on the lowest turn that EXISTS,
		// whatever it is called, rather than on a number chosen here.
		if !t.atAriaFloor() {
			return 0, nodeRef{}, jumpOlder
		}
		// Standing on the floor with a hole above the oldest message means the
		// beginning is INSIDE the hole. Wait for it rather than landing on the
		// sentinel that stands where it will be.
		if entries[0].isGap() {
			return 0, nodeRef{}, jumpOlder
		}
		e := &entries[0]
		return e.start, t.firstRefOfTurn(e.turn), jumpHere
	}
	switch {
	case tg.turn < oldest:
		// A hole below the oldest loaded turn may BE the target's home.
		if t.leadingGap() {
			return 0, nodeRef{}, jumpOlder
		}
		if t.atAriaFloor() {
			return 0, nodeRef{}, jumpAbsent
		}
		return 0, nodeRef{}, jumpOlder
	case tg.turn > newest:
		// The window reaches the live tail, so there is nothing newer to load.
		return 0, nodeRef{}, jumpAbsent
	}
	if tg.hasNode {
		ref := nodeRef{turn: tg.turn, index: tg.node}
		if span, ok := t.nodeSpanOf(ref); ok {
			return span.first, ref, jumpHere
		}
		// The turn is here but that node is not. It can only be below the
		// oldest retained SLICE of it, a turn too tall for one page arrives in
		// slices, and the head slice is the one that got trimmed.
		if tg.turn == oldest && tg.node < int(t.oldestFrom()) && !t.atAriaFloor() {
			return 0, nodeRef{}, jumpOlder
		}
		if t.hasGap() {
			return 0, nodeRef{}, jumpOlder
		}
		return 0, nodeRef{}, jumpAbsent
	}
	for k := range entries {
		if !entries[k].isGap() && entries[k].turn == tg.turn {
			return entries[k].start, t.firstRefOfTurn(tg.turn), jumpHere
		}
	}
	// Between the bounds and not in the index: the turn is inside a hole, and
	// the only honest answer is "not yet".
	if t.hasGap() {
		return 0, nodeRef{}, jumpOlder
	}
	return 0, nodeRef{}, jumpAbsent
}

// windowTurnBounds is the oldest and newest TURN in the window, ignoring gap
// entries: whose turn field is 0, an id no aria ever issues.
func (t *transcript) windowTurnBounds() (oldest, newest int, ok bool) {
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.isGap() {
			continue
		}
		if !ok {
			oldest, newest, ok = e.turn, e.turn, true
			continue
		}
		if e.turn < oldest {
			oldest = e.turn
		}
		if e.turn > newest {
			newest = e.turn
		}
	}
	return oldest, newest, ok
}

// leadingGap reports whether a hole stands above the oldest message in the
// window: i.e. whether history that is "below the floor" as far as the reader
// is concerned is actually a hole INSIDE it.
func (t *transcript) leadingGap() bool {
	return len(t.index.entries) > 0 && t.index.entries[0].isGap()
}

// hasGap reports whether the window is missing anything inside itself. (whole()
// asks a bigger question: no holes AND standing on the floor, and a jump only
// cares about the holes.)
func (t *transcript) hasGap() bool {
	for k := range t.index.entries {
		if t.index.entries[k].isGap() {
			return true
		}
	}
	return false
}

// oldestGap is the first hole in line space. A jump asks for THIS one rather
// than gapNear's: a walk is heading for a coordinate it cannot see yet, so the
// hole to close is the one nearest the beginning, not the one nearest the eye.
func (t *transcript) oldestGap() *aria.Gap {
	for k := range t.index.entries {
		if e := &t.index.entries[k]; e.isGap() {
			return e.gap
		}
	}
	return nil
}

// firstRefOfTurn is the turn's first selectable point in reading order: its
// question when it has one, otherwise its first node. That is what the jump
// puts the selection on, so a turn-granular landing still names something.
func (t *transcript) firstRefOfTurn(turn int) nodeRef {
	for _, p := range t.nodeRefs() {
		if p.turn == turn {
			return p.nodeRef
		}
	}
	return nodeRef{}
}

// landJump snaps the viewport so the target's first row sits at the top, and
// puts the selection on it so the landing is visibly identified.
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

// selectRef puts the selection on one node, carrying the hash the copy path
// verifies endpoints with: which is why it goes through nodeRefs() rather
// than minting a bare point.
func (t *transcript) selectRef(ref nodeRef, extend bool) bool {
	t.wantTop = false // selecting is a deliberate move; see transcript.wantTop
	for _, p := range t.nodeRefs() {
		if p.nodeRef != ref {
			continue
		}
		if !extend || !t.selection.active {
			t.selection.anchor = p
		}
		t.selection.focus = p
		t.selection.active = true
		return true
	}
	return false
}

// abandonJump gives up and puts the reader back where they were, exactly as
// finishSearch does for a search that found nothing. An empty note clears the
// walk silently (a second ':' replacing the first).
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

// noteOrClear puts a note in the message drawer, or closes it when empty.
func (t *transcript) noteOrClear(note string) {
	t.jumpNote = note
	if note == "" {
		if t.showing("message") {
			t.drawer.close()
		}
		return
	}
	t.drawer.showMessage(note)
}

// jumpFooter is what a WALK IN PROGRESS says while it runs. The box itself and
// its failures are drawn by the drawer now (see transcript.inputDrawerLines and
// noteOrClear); this is only the transient "jumping to turn 12…".
func (t *transcript) jumpFooter() (string, bool) {
	if t.jump != nil {
		return "jumping to " + t.jump.target.String() + "…", true
	}
	return "", false
}

// cmdPaste is ^V in the command box: the system clipboard, folded onto one
// line. It reverses the "deliberately unbound" note in keymap.go, and the
// reversal is Gluck's: that argument was that quoting had no use in a box
// where every printable byte is already literal text, and paste is a different
// verb with an obvious one.
func cmdPaste(t *transcript) {
	text, err := clipboardRead()
	if err != nil {
		t.noteOrClear("paste: " + err.Error())
		return
	}
	if text = pasteIntoLine(text); text == "" {
		return
	}
	t.edit(func(e *lineEditor) { e.insert(text) })
}
