package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
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
//
// A HOLE IS NOT A TURN, and this is the only resolver that has to know it. Gap
// entries stand in line space beside messages but carry turn 0 (buildIndex),
// and reading their turn as an address is how `:0` used to land ON the
// "N turns not loaded" rule — the reader asked for the beginning of the aria
// and got a placeholder for it, with no selection, because firstRefOfTurn(0)
// matches nothing. The same zero, read as the window's OLDEST turn, then made
// every `tg.turn < oldest` test false, so a jump to a turn behind the hole was
// answered "no turn N in this aria" — a denial about a conversation the pager
// had simply not loaded yet.
//
// So: bounds come from MESSAGE entries only, and a target that could be inside
// a hole resolves to jumpOlder — keep walking. pageCursor turns that into a
// fill (see the jump branch there), jumpAdvance re-resolves when it lands, and
// the landing happens on the real turn once the entry is ungapped. Delaying is
// the whole trick: there is nothing to snap to until the rows exist.
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
		// on the wire says where an aria begins — an empty ReadBefore is the
		// only proof there is. So this lands on the lowest turn that EXISTS,
		// whatever it is called, rather than on a number chosen here.
		//
		// In practice that number is 1, forks included: a fork can read its
		// parent's prefix, so its history opens where the parent's did. The
		// point is not that 1 is wrong — it is that a client cannot tell 1
		// from any other floor without asking, and asking is a walk.
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
		// oldest retained SLICE of it — a turn too tall for one page arrives in
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
// entries — whose turn field is 0, an id no aria ever issues.
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
// window — i.e. whether history that is "below the floor" as far as the reader
// is concerned is actually a hole INSIDE it.
func (t *transcript) leadingGap() bool {
	return len(t.index.entries) > 0 && t.index.entries[0].isGap()
}

// hasGap reports whether the window is missing anything inside itself. (whole()
// asks a bigger question — no holes AND standing on the floor — and a jump only
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
// verifies endpoints with — which is why it goes through nodeRefs() rather
// than minting a bare point.
//
// extend is the Shift variant: the anchor stays where it was and only the focus
// moves, so a range can be built by pointing at its far end (Shift+click) just
// as it can by walking to it (Shift+^N). A cold selection ignores extend — there
// is no anchor yet to extend from.
//
// It does NOT touch the viewport, deliberately: the jump sets the offset itself
// after detaching (see landJump), and a click is on screen already. Whoever
// calls this owns where the page ends up.
func (t *transcript) selectRef(ref nodeRef, extend bool) bool {
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
