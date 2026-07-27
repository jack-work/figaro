package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// THE `:` JUMP: go to a coordinate.
//
// Ctrl-O draws every node's address (transcript_coords.go). This is the other
// half — a command line that accepts one:
//
//	:12       the head of turn 12
//	:12.3     node 3 of turn 12
//	:12.-1    that turn's question (the inquiryNode sentinel, drawn as -1)
//	:0        THE BEGINNING of the aria
//
// It is wired exactly as `/` is: a keymap MODE (modeJump) with rows for
// accept, cancel and backspace, and everything else falling through to
// literal text. There is no second input loop, and `/` is a literal character
// inside the jump box exactly as `:` is one inside the search box.
//
// `:0` IS A SENTINEL, NOT AN ADDRESS. Turn 0 does not exist: turn ids start at
// 1 and are dense within an aria, but a FORKED aria adopts its parent's
// numbering (internal/turns.StampIDs adopts an already-stamped id), so a
// fork's first turn is emphatically not 1. `:0` therefore means "the lowest
// turn that actually exists, whatever its id" and is resolved by walking back
// until the store says there is nothing older — never by constructing an
// anchor. aria.Anchor.Zero() is likewise UNSET ("the natural end for this
// direction"), which is precisely why turn 0 can never be a real target and
// can safely carry this meaning here.
//
// `gg` is untouched and still means the top of the RETAINED BUFFER. The two
// gestures are different questions once a conversation is long enough to page,
// and conflating them would cost you the cheap one.
//
// WHAT HAPPENS WHEN THE TARGET IS NOT LOADED
//
// The pager grows its window backward — over history the store already holds
// for free, and below that through ReadBefore — driven by
// prefetchTranscriptPages, which CHAINS: it re-asks pageCursor after every
// landing and keeps fetching while the pager says it wants more. A jump
// therefore does not need a mechanism of its own — it only has to keep saying
// "still want one", which is what jumpAdvance does by leaving t.jump set.
// Nothing else in this file knows how pages arrive.
//
// The walk is BOUNDED, and reports rather than hanging or landing somewhere
// else and pretending. See jumpBudget.

// jumpBudget is how many page fetches one jump may spend before giving up.
//
// 24 is chosen against measurements this repo already has, not by feel. One
// fetch is a single ReadBefore round trip to the local daemon over a unix
// socket — 0.1–1.2 ms measured on the long-aria benchmarks — and returns
// pageMessages() messages, which is between transcriptMinPageSize and
// transcriptPageSize. So 24 fetches cover several hundred to a few thousand
// messages, comfortably past the largest real aria measured on this project
// (1624 messages across 38 turns, quoted in committedMessages), while
// bounding the worst case to tens of milliseconds of I/O.
//
// The bound is not really about speed. It is about TERMINATION: a store that
// keeps returning pages which never reach the target would otherwise spin
// forever, and the honest failure — a line in the footer saying so — is worth
// far more than a pager that silently stops responding.
const jumpBudget = 24

// jumpTarget is a parsed coordinate. It is deliberately NOT an aria.Anchor:
// Anchor{Turn: 0} means UNSET on the wire, and `:0` means the opposite of
// unset. Keeping the sentinel in the parser's own type is what stops a
// backward read from being handed a zero anchor and returning the tail.
type jumpTarget struct {
	start   bool // ":0" — the lowest turn that exists
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
// nothing else — a coordinate is not an expression.
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
// same reason — a paged search and a paged jump are one traversal with two
// stopping conditions. As there, only the VIEWPORT is restored: history the
// walk paged in is in the store, and throwing the floor back up would only
// re-fetch it.
//
// There is no jumpNewer. The window runs to the live tail by construction, so
// a target newer than the newest thing in it cannot exist.
type transcriptJump struct {
	target  jumpTarget
	fetches int

	offset int
	follow bool
}

// ---------------------------------------------------------------------------
// The keymap actions. One row each; see keymap.go.
// ---------------------------------------------------------------------------

// pagerJumpPrompt is ':' — open the command line.
func pagerJumpPrompt(t *transcript) { t.inJump, t.jumpQuery, t.jumpNote = true, "", "" }

func jumpCancel(t *transcript) { t.inJump, t.jumpQuery = false, "" }

func jumpBackspace(t *transcript) {
	if len(t.jumpQuery) > 0 {
		t.jumpQuery = t.jumpQuery[:len(t.jumpQuery)-1]
	}
}

// jumpLiteral is the box's fallback: a printable key is text, not a binding —
// which is what makes '/' an ordinary character in here, as ':' is in the
// search box. The keymap does not enumerate "every printable byte"; see
// searchLiteral, whose contract this mirrors exactly.
func (t *transcript) jumpLiteral(b byte) {
	if b >= 0x20 && b < 0x7f {
		t.jumpQuery += string(b)
	}
}

// jumpAccept is Enter in the jump box.
func jumpAccept(t *transcript) {
	text := t.jumpQuery
	t.inJump, t.jumpQuery, t.jumpNote = false, "", ""
	tg, err := parseJumpTarget(text)
	if err != nil {
		t.jumpNote = err.Error()
		return
	}
	t.startJump(tg)
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
		t.jumpNote = tg.missing()
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
	if tg.start {
		// The floor is only known once the store has said so. Note that it is
		// NOT "turn 1": a forked aria continues its parent's numbering, and an
		// empty ReadBefore proves the floor exactly so the lowest EXISTING turn
		// is what we land on, whatever it is called.
		if !t.atAriaFloor() {
			return 0, nodeRef{}, jumpOlder
		}
		e := &entries[0]
		return e.start, t.firstRefOfTurn(e.turn), jumpHere
	}
	oldest, newest, haveTurns := t.turnBounds()
	if !haveTurns {
		if t.atAriaFloor() {
			return 0, nodeRef{}, jumpAbsent
		}
		return 0, nodeRef{}, jumpOlder
	}
	switch {
	case tg.turn < oldest:
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
		// oldest retained SLICE of it — a turn too tall for one page arrives in
		// slices, and the head slice is the one that got trimmed.
		if tg.turn == oldest && tg.node < int(t.oldestFrom()) && !t.atAriaFloor() {
			return 0, nodeRef{}, jumpOlder
		}
		return 0, nodeRef{}, jumpAbsent
	}
	for k := range entries {
		if entries[k].turn == tg.turn {
			return entries[k].start, t.firstRefOfTurn(tg.turn), jumpHere
		}
	}
	return 0, nodeRef{}, jumpAbsent
}

// turnBounds is the lowest and highest TURN in the index, and whether there is
// one at all.
//
// NOT entries[0] and entries[len-1]. An entry does not necessarily carry a
// turn: a GAP stands for turns we do not hold, and an ECHO is a prompt with no
// coordinate at all — both record turn 0, and an echo is the LAST entry
// whenever one is on screen. Reading the ends directly made `newest` zero the
// moment a prompt was submitted, so every `:12` answered "absent" while turn 12
// was plainly on the screen.
func (t *transcript) turnBounds() (oldest, newest int, ok bool) {
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.turn == 0 {
			continue
		}
		if !ok || e.turn < oldest {
			oldest = e.turn
		}
		if !ok || e.turn > newest {
			newest = e.turn
		}
		ok = true
	}
	return oldest, newest, ok
}

// firstRefOfTurn is the turn's first selectable point in reading order — its
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
//
// ensureSelectionVisible is deliberately NOT called. It scrolls to reveal a
// selection's far END, so on a node taller than the pane it would drag the
// viewport straight past the row we just aimed at — the opposite of a snap.
// The offset is set last for the same reason: stopFollowing re-derives it for
// the detached geometry, and would otherwise overwrite the landing.
func (t *transcript) landJump(line int, ref nodeRef) {
	t.jumpNote = ""
	t.stopFollowing()
	if ref.valid() {
		t.selectRef(ref)
	}
	if line < 0 {
		line = 0
	}
	t.offset = line
}

// selectRef puts the selection on one node, carrying the hash the copy path
// verifies endpoints with — which is why it goes through nodeRefs() rather
// than minting a bare point.
func (t *transcript) selectRef(ref nodeRef) bool {
	for _, p := range t.nodeRefs() {
		if p.nodeRef == ref {
			t.selection = nodeSelection{active: true, anchor: p, focus: p}
			return true
		}
	}
	return false
}

// abandonJump gives up and puts the reader back where they were, exactly as
// finishSearch does for a search that found nothing. An empty note clears the
// walk silently (a second ':' replacing the first).
func (t *transcript) abandonJump(note string) {
	if t.jump == nil {
		t.jumpNote = note
		return
	}
	origin := t.jump
	t.jump = nil
	t.offset = origin.offset
	t.follow = origin.follow
	t.jumpNote = note
	t.pruneCaches()
}

// jumpFooter is the status row while a jump owns it: the box being typed
// into, the walk in progress, or what went wrong. Empty means the ordinary
// status line keeps the row.
func (t *transcript) jumpFooter() (string, bool) {
	switch {
	case t.inJump:
		return ":" + t.jumpQuery, true
	case t.jump != nil:
		return "jumping to " + t.jump.target.String() + "…", true
	case t.jumpNote != "":
		return t.jumpNote, true
	}
	return "", false
}
