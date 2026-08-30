package cli

import (
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	"github.com/jack-work/figaro/internal/term"
)

const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
)

// transcript is a full-screen, live-updating pager over a paged conversation,
// drawn on the alternate screen and toggled with Ctrl-T. Because it owns a fixed
// canvas with a bounded message window, it is both resize-clean and scrollable
// without retaining or re-rendering the whole aria. At the bottom it follows
// the shared client's live tail; otherwise it holds the current page window.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client
	status *sessionStatus

	active bool
	// pit is THE transient region below the transcript: help, figaro status,
	// the queue, a command's output, an error, the completion list. One at a
	// time, Esc closes it, and none of them may touch the status bar. See
	// pit.go for why that last clause is the point of the type.
	pit         pit
	queuedByKey bool // ...because the user pressed 'Q', not because it filled

	// full is FULLSCREEN, and it is the pager's, not the pit's: `F` sets a
	// disposition that whatever pit is open inherits, so closing a form and
	// opening the queue does not quietly change how much of the screen a
	// reader gets. It applies only while the PIT has the focus.
	full bool
	// focused says whether keys and the screen belong to the pit or to the
	// conversation behind it. `T` moves it. A fullscreen pit RECEDES when the
	// transcript takes focus rather than closing: the reader asked to look
	// past it, not to lose it. Gluck: "the pit should remain open."
	focused pitFocus
	// queueDismissed latches a DELIBERATE close of the queue pit, so the
	// next poll does not put it straight back. This is Gluck's bug: `fig send`
	// that queues a message and enters the pager showed the queue, Esc closed
	// it, and the very next figaro.queued reopened it -- because the queue was
	// still non-empty and nothing anywhere recorded that the reader said no. An
	// auto-opening panel needs an auto-open SUPPRESSOR or it cannot be
	// dismissed at all.
	queueDismissed bool
	queuedRows     []string     // pre-rendered by livelogTurn, for the inline trailer
	queued         []queuedItem // the queue itself, ids intact, for the pit
	queuedFetch    func()       // async refresh of the queued snapshot; set by the input loop
	// command runs a ':' line that is not a coordinate. Set by the input loop,
	// which owns the RPC clients; nil in a fixture, where the box still takes
	// coordinates and says so for anything else.
	//
	// IT IS CALLED UNDER THE RENDER LOCK, like every other key action, so an
	// implementation MUST hand off rather than work: taking the lock deadlocks
	// the input goroutine against itself, and blocking on an RPC stops the
	// keyboard. See interactiveInput.runCommand, which is one `go` statement
	// for exactly this reason.
	command func(string)
	// completer returns Tab candidates for a partially typed command line. Set
	// by the input loop, which owns the router.
	completer func(string) []string
	// openForm is 'S': the input loop's door to the live form view, because
	// opening one dials. IT MUST NOT TAKE THE RENDER LOCK -- every hook here
	// is called from dispatch, which already holds it, so implementations hand
	// off to a goroutine. `S` froze the pager dead until they did.
	openForm func()
	// dropRow is 'x' on a selected pit row: the owner decides what dropping
	// means for that pit's name.
	dropRow func(pit, id string)
	w, h    int
	tick    int

	prev   []string // last painted screen (the frame the terminal is holding)
	prefix string   // one-shot escapes emitted with the next frame (see enter)
	// altPending/altOn are the alt-screen PAIRING. enter() queues the switch in
	// prefix and sets altPending; the paint that actually emits it sets altOn;
	// leave() writes the exit sequence only when altOn. Without this, a pager
	// that never painted (h < 4, or a frame deferred behind the rate gate) still
	// wrote 2J + ?1049l: to the user's real screen.
	altPending bool
	altOn      bool
	// resync clock: the painter's model of the screen (prev) is only true while
	// figaro is the ONLY writer to the terminal. It is not. See screenMoved and
	// resyncDue: lastFull is when the screen was last painted unconditionally,
	// and now is injectable so the interval is testable without sleeping.
	now      func() time.Time
	lastFull time.Time
	lineKey  []sliceKey // slice owning each line of lines(), for resize anchoring
	offset   int        // top line of the viewport into lines()
	// wantTop is a STANDING request for the beginning, armed by Home/gg.
	wantTop bool
	follow  bool // stick to the bottom on new content
	pendG   bool // saw one 'g' (for gg)

	// Frame scheduling. render() marks the screen stale and defers when a
	// batch is open (an input burst being drained) or when the frame-rate gate
	// declines; flush() draws the deferred frame. See beginBatch/endBatch.
	batch   int
	dirty   bool
	gate    func() bool // "may I paint now?", a false answer owes a later flush()
	painted func()      // notified after each painted frame

	inSearch   bool
	query      string
	matchQuery string // persistent query: highlights + n/N target

	// The ':' coordinate jump (transcript_jump.go). inJump/jumpQuery are the
	// command line, exactly as inSearch/query are the search box; jump is a
	// walk in progress; jumpNote is what the footer says about the last one.
	inJump bool
	// cmdline is the ':' box's editor: runes, a cursor, emacs motions and
	// history. It replaced a `q += string(b)` string for the reasons in
	// lineedit.go -- the short one being that the old box could not represent
	// "café", let alone a cursor.
	cmdline lineEditor
	// completions is the last Tab's candidate list and which of them is
	// selected. THE MENU IS BOUNDED: it draws at most completionMenuRows rows
	// inside the pit, because a completion list that grows with the number
	// of arias is a completion list that eats the screen -- which is what the
	// first cut did with forty verbs.
	completions   []string
	completionIdx int // -1 = nothing selected; the common prefix was inserted
	completionAt  int // rune index where the completed word starts
	jumpNote      string
	jump          *transcriptJump

	// Lazy history paging: the pager opens on the store's tail and pulls older
	// history via keyset ReadBefore only when the viewport comes near the window
	// floor ("like Twitter").
	search *transcriptSearch

	// THE WINDOW IS THE STORE'S OWN TAIL, not a copy of it: the half-open
	// interval [from, ∞), whose floor is the anchor of its oldest message. There
	// is no second copy of anything, in either direction.
	from aria.Anchor

	// tailTuned latches the one row-budget retune allowed per tail window.
	tailTuned bool
	// tailWant is the tuned tail-window size in messages (0 = not yet tuned).
	tailWant int
	// windowRev is THE authority on "the retained window changed". Every
	// mutation of the window's floor goes through invalidateWindow, which bumps
	// this counter so the line index refills lineKey instead of inferring the
	// move from a shape diff.
	windowRev uint64

	// rowCache memoizes rows of committed messages in their unselected resting
	// form: clipped and gutter-prefixed (plainNodeRow), but carrying no
	// selection cue and no search highlight. That keeps the cache a pure
	// function of (message, width), so selection and search state can change
	// without invalidating a single cached row; the cue and the highlight are
	// applied per painted row by window()/entryLine().
	rowCache  map[sliceKey]cachedMessage
	cacheW    int
	selection nodeSelection
	expanded  map[nodeRef]bool

	// index is the viewport virtualization: a per-frame map from line space to
	// message rows, rebuilt in O(#messages) so scrolling never re-materializes
	// (or re-decorates) rows it will not paint. See transcript_index.go.
	index   lineIndex
	ruleStr string // memoized separator rule for ruleW columns
	ruleW   int
	// Reused buffers. Each has a distinct owner so no two of them can ever
	// alias: rowBuf belongs to render (the visible window), lineBuf to lines()
	// (the whole-window materialization, off the frame path), paintBuf to the
	// painter, and screenSpare is the composed frame paint displaced when it
	// retained the previous one as t.prev.
	rowBuf      []string     // the visible window, valid until the next render()
	frameRefs   []nodeRef    // the node behind each BODY row of the last painted frame
	lineBuf     []string     // whole-window rows, valid until the next lines()
	keepBuf     map[int]bool // reused live-turn set for pruneCaches
	paintBuf    []byte       // reused escape-sequence output buffer
	predBuf     []string     // predicted grid after a scroll-region shift
	keysNew     []uint32     // row fingerprints, screen side (shift detection)
	keysOld     []uint32     // row fingerprints, prev side
	screenSpare []string     // the frame buffer displaced by the last paint
}

// transcriptSearch is a paged search in progress: the query, and enough of the
// origin to put the reader back where they were if the walk finds nothing.
// Only the VIEWPORT is restored: history the walk paged in stays in the store.
type transcriptSearch struct {
	query  string
	offset int
	follow bool
}

// transcriptPageRequest is one backward read: the keyset cursor to read before,
// and how many messages to ask for. There is no direction any more: the window
// runs to the live tail by construction, so the only history that can be
// missing is OLDER.
type transcriptPageRequest struct {
	before int
	// beforeNode is the node offset of `before`: the oldest retained slice can
	// start MID-TURN (a page clipped at its head), and asking for what precedes
	// the TURN would skip the rest of it forever: its head nodes and the
	// inquiry drawn above them.
	beforeNode int
	limit      int // messages to fetch; 0 means transcriptPageSize
	// fill is a HOLE INSIDE the window rather than history below its floor.
	// They are different verbs: a floor read EXTENDS the window, a fill CLOSES
	// a hole and is served by Client.Ensure, which keeps reading until the hole
	// is gone or the store says it cannot shrink it.
	fill *aria.Gap
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	return &transcript{
		out: out, view: view, client: client,
		status: newSessionStatus(figaroID, startedAt), w: w, h: h,
		rowCache: map[sliceKey]cachedMessage{}, expanded: map[nodeRef]bool{},
	}
}

// enter switches to the alternate screen and draws the transcript at the bottom.
// autowrapOff is asserted explicitly here (not just inherited from the caller):
// this is a fixed-canvas pager, and a single wide glyph tipping the bottom-right
// cell past the last column with autowrap ON scrolls the whole screen up by a
// row: which reads as the status line "eating" the line above it.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query, t.matchQuery = false, false, "", ""
	t.inJump, t.jumpNote, t.jump = false, "", nil
	t.cmdline.reset()
	t.completions = nil
	// THE PAGER OWNS RETENTION while it is up. The client's count-based trim
	// drops the OLDEST messages, which is precisely wrong once the window is the
	// store itself: a reader scrolled up, or a catch-up page merged in to open on
	// history, would be trimmed out from under. evictStale keeps the store bounded
	// against the window instead; leave() hands the count limit back.
	t.client.SetClosedLimit(0)
	t.from = aria.Anchor{}
	t.invalidateWindow() // a fresh session always rebuilds the window from the tail
	t.resetToTail()
	// QUEUED, NOT WRITTEN: the switch rides the next frame, so a pager that
	// never paints never switches the terminal at all. leave() must honour the
	// same condition: that is what altPending/altOn record.
	t.prefix = altScreenOn + ldmouse.Enable
	t.altPending = true
	t.render()
}

// leave restores the normal screen: but ONLY if enter() ever actually reached
// it. Mouse reporting is disabled before the alt-screen swap so no stray
// \x1b[<…M leaks into the shell.
func (t *transcript) leave() {
	t.active = false
	t.selection = nodeSelection{} // no selection survives outside the pager
	t.prefix = ""                 // a frame that never painted must not switch us back
	t.altPending = false
	t.client.SetClosedLimit(transcriptTailLimit)
	if t.altOn {
		io.WriteString(t.out, ldmouse.Disable+altScreenOff)
		t.altOn = false
	}
	t.prev = nil
}

// scroll moves the viewport by delta lines. Detaching comes FIRST:
// stopFollowing pins the offset at the live view's own position, so the motion
// is relative to what is on screen. Moving the offset first instead scrolled
// from a stale value: zero on a pager that had not painted a frame yet, which
// is how one 'k' from the inline view landed at the top of a window it had
// never seen.
func (t *transcript) scroll(delta int) {
	t.wantTop = false
	t.stopFollowing()
	t.offset += delta
	if _, maxOff := t.layout(len(t.footLines())); t.offset > maxOff {
		pagerTail(t)
	}
}

// scrollNotch is the single-notch gesture (k/Up, one wheel notch). Upward out
// of live, DETACHING IS THE MOTION: stopFollowing hands the live padding row
// back to content, and that row IS the notch of travel spent: the window keeps
// its last line, so nothing scrolls away and the only change on screen is the
// blank row above the rule becoming content while the footer drops 'live'. The
// next notch scrolls normally. Deliberate jumps (u/PgUp, gg) do not route
// through here: a jump should land where it lands.
func (t *transcript) scrollNotch(delta int) {
	if t.follow && delta < 0 {
		t.stopFollowing()
		return
	}
	t.scroll(delta)
}

// scrollBy is the native wheel's entry point; render clamps the offset (see
// render, which clamps the low side even when the frame itself is deferred).
func (t *transcript) scrollBy(delta int) {
	if !t.active {
		return
	}
	t.scrollNotch(delta)
	t.render()
}

// transcriptPageSize is the ceiling on how many older messages a single
// scroll-up fetch pulls; transcriptWindowRows is the real budget.
const (
	transcriptPageSize    = 30
	transcriptMinPageSize = 6
	transcriptPageLimit   = 3
	transcriptTailLimit   = 2 * transcriptPageSize
)

// transcriptWindowRows is the retained-window budget in rendered rows. A var,
// not a const, so the geometry sweep in transcript_geometry_bench_test.go can
// measure the tradeoff it encodes. See skills/figaro/contributing/notes/transcript-paging.md for the
// numbers behind the chosen value.
var transcriptWindowRows = 2400

// heldWindow is the EXACT size of the retained window in line space: rendered
// rows (inter-message rules included) and the number of committed messages
// they belong to. It reads axis A's line index, which already computes both as
// a side effect of every frame.
func (t *transcript) heldWindow() (rows, messages int) {
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if e.open {
			continue
		}
		rows += e.height()
		messages++
	}
	return rows, messages
}

// avgRowsPerMessage is the measured height of the messages the retained window
// holds, or 0 when the index has not been built yet (a cold pager).
func (t *transcript) avgRowsPerMessage() int {
	rows, n := t.heldWindow()
	if n == 0 {
		return 0
	}
	if avg := rows / n; avg > 0 {
		return avg
	}
	return 1
}

// pageRowBudget is the row budget of ONE page: the retained window is
// transcriptPageLimit pages, and the tail window is exactly one.
func pageRowBudget() int { return transcriptWindowRows / transcriptPageLimit }

// pageMessages is how many messages one page should hold so that a full window
// (transcriptPageLimit pages) lands near the row budget. Falls back to the
// message-count ceiling until we have measured anything.
func (t *transcript) pageMessages() int {
	avg := t.avgRowsPerMessage()
	if avg <= 0 {
		return transcriptPageSize
	}
	n := pageRowBudget() / avg
	if n > transcriptPageSize {
		n = transcriptPageSize
	}
	if n < transcriptMinPageSize {
		n = transcriptMinPageSize
	}
	return n
}

// tailKeep is how many messages the tail window holds. Cold (nothing rendered
// yet, so no idea how tall this aria's messages are) it deliberately starts at
// the floor rather than the ceiling: opening the pager on an aria of 400-row
// tool dumps used to render thirty of them to paint one screen. tuneTail then
// grows the window into the row budget, which is invisible because the added
// rows are ABOVE a viewport pinned to the tail.
func (t *transcript) tailKeep() int {
	if t.tailWant > 0 {
		return t.tailWant
	}
	// COLD: nothing rendered, so no height is known and every number here would
	// be invented. Start at the floor and let tuneTail walk up, which is
	// invisible: the rows it adds are ABOVE a viewport pinned to the tail.
	return transcriptMinPageSize
}

// transcriptPrefetchScreens is how close (in viewports) the scroll position has
// to get to an edge of the retained window before we start pulling the next
// page. One screen means the fetch is armed only once the user is already
// looking at the last rows we have, so the RPC lands *after* they hit the wall;
// two gives a screenful of runway, which at wheel speed is a few hundred
// milliseconds: enough to cover a local daemon round trip.
const transcriptPrefetchScreens = 2

// wantOlder reports whether anything is asking for history below the window
// floor. Three askers, and no flags: a search or a jump walking backward, or a
// viewport that has come within the prefetch distance of the floor.
func (t *transcript) wantOlder() bool {
	if t.search != nil || t.jump != nil {
		return true
	}
	return t.offset < transcriptPrefetchScreens*t.h
}

// atAriaFloor reports whether the window already stands on the beginning of
// the aria: the store holds nothing below the floor and the wire says there is
// nothing before it.
func (t *transcript) atAriaFloor() bool {
	if t.client.Count() == 0 {
		return !t.client.MoreBefore()
	}
	if t.from.Turn <= 1 && t.from.Node == 0 {
		return true
	}
	if _, held := t.client.Before(t.from, 1); held > 0 {
		return false
	}
	return !t.client.MoreBefore()
}

// growWindow lowers the floor over history THE STORE ALREADY HOLDS, and reports
// the messages it gained. This is the job the payload LRU used to do for the
// return trip, except that with one owner there is nothing to hold a second
// copy in: the messages are in the store, their rows are still in the cache,
// and the whole move is one backward walk and a new floor.
func (t *transcript) growWindow(n int) []aria.Message {
	if n <= 0 {
		return nil
	}
	floor, held := t.client.Before(t.from, n)
	if held == 0 {
		return nil
	}
	gained := make([]aria.Message, 0, held)
	t.client.ForEachIn(floor, t.from.Prev(), func(m aria.Message) bool {
		gained = append(gained, m)
		return true
	})
	t.lowerFloor(floor)
	return gained
}

// lowerFloor moves the window's floor down onto a. It never raises it: that is
// resetToTail's job, and only while following.
func (t *transcript) lowerFloor(a aria.Anchor) {
	if !a.Less(t.from) {
		return
	}
	t.from = a
	t.invalidateWindow()
}

// reachedFloor is what to do when the walk can go no further: a search that
// found nothing goes home, and a jump resolves against the beginning it was
// waiting for (`:0` lands on the lowest turn that actually exists).
func (t *transcript) reachedFloor() {
	t.wantTop = false
	t.finishSearch(false)
	t.jumpAdvance()
	t.render()
}

// pageCursor asks whether the pager wants a page of older history, and from
// where. It is called after every input chunk and again after every landing;
// the answer is derived, not remembered.
func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	// GROWING THE WINDOW MUST MAKE PROGRESS, or this loop is a spin with no
	// I/O in it -- one core pinned inside the render lock, which is what a
	// frozen pager with a loud fan actually is.
	//
	// growWindow lowers the floor over history the store already holds;
	// absorbOlder ends in buildIndex, which -- while FOLLOWING -- resets the
	// window to the tail and puts the floor straight back up. Measured on a
	// live freeze: the floor went 115 -> 91 -> 115 -> 91, with the store
	// holding thirty messages and the tail keeping six, and wantOlder() true
	// throughout because the viewport was at the top of what was held.
	//
	// So the loop asks the only honest question: did the floor actually go
	// down? If it did not, there is nothing more to take from the store and
	// the walk continues on the wire (or stops).
	for t.active && t.wantOlder() {
		anchor, within := t.viewportAnchor()
		before := t.from
		gained := t.growWindow(t.pageMessages())
		if len(gained) == 0 {
			break
		}
		t.absorbOlder(gained, anchor, within)
		if !t.from.Less(before) {
			break
		}
	}
	// A HOLE INSIDE THE WINDOW comes first: the reader is looking at it, or is
	// about to. Closing it is Ensure's job, not a floor read, so the request
	// carries the hole itself.
	if gap := t.gapNear(); gap != nil {
		hole := *gap
		return transcriptPageRequest{fill: &hole}, true
	}
	// A WALK DRIVES ITS OWN FILL. gapNear reports only the hole the VIEWPORT is
	// about to paint; a jump is heading for a coordinate it cannot see yet, and
	// its target may be inside a hole two screens away: jumpReachOf answers
	// "not yet" for exactly that case. Without this branch that answer has
	// nothing to wait for: atAriaFloor below is true (the hole is INSIDE the
	// window, not under it), so no page is requested and the walk stands there
	// saying "jumping to the beginning…" forever. It spends budget like any other
	// fetch, so a hole that refuses to close ends in the honest failure rather
	// than a spin.
	if t.jump != nil {
		if gap := t.oldestGap(); gap != nil {
			if t.jump.fetches <= 0 {
				t.abandonJump(fmt.Sprintf("%s is more than %d pages away: scroll or search for it",
					t.jump.target, jumpBudget))
				t.render()
				return transcriptPageRequest{}, false
			}
			t.jump.fetches--
			hole := *gap
			return transcriptPageRequest{fill: &hole}, true
		}
	}
	if !t.wantOlder() {
		return transcriptPageRequest{}, false
	}
	if t.atAriaFloor() {
		t.reachedFloor()
		return transcriptPageRequest{}, false
	}
	if t.jump != nil {
		if t.jump.fetches <= 0 {
			t.abandonJump(fmt.Sprintf("%s is more than %d pages away: scroll or search for it",
				t.jump.target, jumpBudget))
			t.render()
			return transcriptPageRequest{}, false
		}
		t.jump.fetches--
	}
	return transcriptPageRequest{
		before: int(t.from.Turn), beforeNode: int(t.from.Node), limit: t.pageMessages(),
	}, true
}

// absorbOlder is what every arrival of older history does once it is IN the
// window, whether it came from the store or from the wire: re-anchor the
// viewport on the line it was showing, let a search look at what arrived, and
// let a jump resolve against it.
func (t *transcript) absorbOlder(gained []aria.Message, anchor sliceKey, within int) {
	if t.search != nil {
		if t.findPage(t.search.query, gained) {
			t.search = nil
		}
		return
	}
	t.buildIndex()
	if t.wantTop {
		// The reader asked for the beginning and has not asked for anything
		// since. Hold them at the top of what is now held; the floor clears it.
		t.offset = 0
		if t.atAriaFloor() {
			t.wantTop = false
		}
	} else {
		t.restoreViewportAnchor(anchor, within)
	}
	t.jumpAdvance()
}

// applyPage folds a fetched page of older history into the ONE owner and drops
// the window's floor onto it.
func (t *transcript) applyPage(req transcriptPageRequest, page historyPage) {
	if !t.active {
		return
	}
	if len(page.msgs) == 0 {
		// An empty ReadBefore IS the floor, and it is the only way to find it:
		// nothing on the wire reports where an aria BEGINS. Page.More is two
		// booleans: there is more before, there is more after: so the floor
		// can be proven only by asking for what is below it and being handed
		// nothing.
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	anchor, within := sliceKey(0), 0
	if t.search == nil {
		anchor, within = t.viewportAnchor()
	}
	t.client.SetMoreBefore(page.more)
	t.client.Merge(page.msgs, page.extents)
	// A PAGE THAT DOES NOT LOWER THE FLOOR IS THE FLOOR. Every other exit from
	// the paging loops is a fact about the ANSWER -- an empty page, an error --
	// and none of them fires when a read keeps handing back what the pager
	// already holds while still claiming there is more before it. The loop then
	// reads forever, taking the render lock on every pass: a pinned core, a
	// keyboard that does nothing, a pane that will not repaint on resize.
	//
	// Progress is the floor moving. If it does not move, the walk is over,
	// whatever the wire says about `more`: a search reports no match, a jump
	// resolves against the beginning that actually exists, and the reader gets
	// their pager back.
	before := t.from
	t.lowerFloor(anchorOf(page.msgs[0]))
	if t.from == before {
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	t.absorbOlder(page.msgs, anchor, within)
	if t.search != nil {
		return // still walking; the worker asks for the next page
	}
	t.render()
}

// anchorOf is a message's address: the pair (Turn, From) that identifies it
// everywhere in this codebase.
func anchorOf(m aria.Message) aria.Anchor {
	return aria.Anchor{Turn: uint64(m.Turn), Node: m.From}
}

// historyPage is a fetched page folded into the pager's units, WITH the turn
// extents the wire stated. The extents are what let the store decide that the
// last node of turn t and the first of turn t+1 are neighbours rather than a
// hole: an anchor cannot answer that on its own (skills/figaro/contributing/range-store.md,
// "Adjacency is NOT decidable from an anchor"), and a page clipped at its tail
// states nothing, so the map is deliberately partial.
type historyPage struct {
	msgs    []aria.Message
	extents map[int]uint64
	// more is the wire's own answer to "is there anything before this page"
	// (Page.More.Before). It is the only honest source for it, an anchor cannot
	// know, and the pager's old noMoreOlder mirrored it into a latch that every
	// window move had to remember to reset.
	more bool
}

// committedPage is committedMessages plus those extents: the fold used
// wherever the result is going into the store rather than straight to a
// renderer.
func committedPage(p aria.Page) historyPage {
	out := historyPage{msgs: committedMessages(p), more: p.More.Before}
	for _, part := range p.Parts {
		if part.ClippedTail || !part.Sealed {
			continue
		}
		n := part.From + uint64(len(part.Nodes))
		if n == 0 {
			if part.Inquiry == "" {
				continue
			}
			n = 1 // a turn that produced nothing still occupies its phantom node 0
		}
		if out.extents == nil {
			out.extents = map[int]uint64{}
		}
		out.extents[int(part.ID)] = n
	}
	return out
}

// committedMessages flattens a page's parts into the pager's materialized
// units. A part carrying nodes is content; a bare marker carries none and is
// skipped. Message.Turn holds the turn id; Message.From holds the node offset
// within it, so the slices of one tall turn stay distinct units.
func committedMessages(p aria.Page) []aria.Message {
	messages := make([]aria.Message, 0, len(p.Parts))
	for _, part := range p.Parts {
		// The inquiry belongs to the slice that STARTS the turn; a part clipped
		// off the head of one must not repeat it. A part with no nodes still
		// carries its question: that is a turn that produced nothing.
		inquiry := ""
		if !part.ClippedHead && part.From == 0 {
			inquiry = part.Inquiry
		}
		// Turn-level deltas travel with the inquiry: the slice that starts
		// the turn, and no other.
		var turnDeltas map[string]livedoc.FormDelta
		if part.From == 0 {
			turnDeltas = part.FormDeltas
		}
		if len(part.Nodes) == 0 {
			if inquiry != "" || len(turnDeltas) > 0 {
				messages = append(messages, aria.Message{
					Turn: int(part.ID), Inquiry: inquiry, FormDeltas: turnDeltas, Role: livedoc.RoleInput,
				})
			}
			continue
		}
		messages = appendTurnSlices(messages, part.ID, part.From, inquiry, turnDeltas, part.Nodes)
	}
	return messages
}

// transcriptUnitChars bounds one pager unit's payload. Characters, not rows,
// so the split is a pure function of the page: no width, no renderer, no
// per-frame cost. It only has to bound the unit, not measure it exactly.
const transcriptUnitChars = 40000

func nodeChars(n livedoc.Node) int {
	return len(n.Markdown) + len(n.Output) + len(n.Summary)
}

// appendTurnSlices cuts a turn into bounded units at node boundaries,
// appending into dst. A node is never split: the smallest unit is one node,
// however large, because tool output is already clamped by composeBashCap.
func appendTurnSlices(dst []aria.Message, id uint64, from uint64, inquiry string, turnDeltas map[string]livedoc.FormDelta, nodes []livedoc.Node) []aria.Message {
	unit := func(off int, seg []livedoc.Node) aria.Message {
		m := aria.Message{
			Turn: int(id), From: from + uint64(off), Role: livedoc.RoleOutput, Nodes: seg,
		}
		if m.From == 0 {
			m.Inquiry = inquiry
			m.FormDeltas = turnDeltas
		}
		return m
	}
	total := 0
	for _, n := range nodes {
		total += nodeChars(n)
	}
	if total < transcriptUnitChars {
		return append(dst, unit(0, nodes))
	}
	start, budget := 0, 0
	for i, n := range nodes {
		budget += nodeChars(n)
		if budget < transcriptUnitChars && i < len(nodes)-1 {
			continue
		}
		// From is absolute within the turn: the segment's offset inside it plus
		// the part's own. Node ids are positional (Nodes[i].ID == From+i) and
		// sliceKey packs From, so an off-by-one here corrupts the row cache
		// silently.
		dst = append(dst, unit(start, nodes[start:i+1]))
		start, budget = i+1, 0
	}
	return dst
}

// sliceTurn is the standalone form, for tests and callers without a dst.
func sliceTurn(id uint64, from uint64, nodes []livedoc.Node) []aria.Message {
	return appendTurnSlices(nil, id, from, "", nil, nodes)
}

// sliceKey identifies one pager unit: the turn id in the high bits, the node
// offset within that turn in the low 20. Packed into one integer rather than a
// struct because the row cache is a per-frame hot path and a two-word key
// measurably slowed selection rehydrate (+40% at 10k messages). 2^20 nodes in a
// single turn is not reachable; composeBashCap bounds a node long before that.
type sliceKey int64

const sliceKeyFromBits = 20

func keyOf(m aria.Message) sliceKey {
	return sliceKey(int64(m.Turn)<<sliceKeyFromBits | int64(m.From))
}

// turn is the turn id a unit belongs to.
func (k sliceKey) turn() int { return int(k >> sliceKeyFromBits) }

// resetToTail re-points the window at the STORE's tail. It does not copy: the
// window is the interval [from, ∞) and the store is its only holder, so
// "rebuilding" it is recomputing one anchor: the floor, tailKeep messages
// back from the end, which Store.TailFrom walks BACKWARD and which therefore
// costs the window's own size, not the aria's.
func (t *transcript) resetToTail() {
	t.wantTop = false
	keep := t.tailKeep()
	n := t.client.Count()
	if keep > n {
		keep = n
	}
	from, _ := t.client.TailFrom(keep)
	if t.from == from {
		return // the window already IS the tail at this floor
	}
	t.from = from
	t.invalidateWindow()
	t.tailTuned = false
	t.evictStale()
	t.pruneCaches()
}

// transcriptRetainRows is how many rendered rows the STORE is allowed to hold
// behind the pager's window: the return trip's worth. It plays the part
// transcriptPayloadLRU plays for fetched pages: what the store still holds,
// the row cache still holds rows for, so scrolling back up costs neither I/O
// nor a re-render. Four windows, which is what the LRU was sized at.
var transcriptRetainRows = 4 * transcriptWindowRows

// retainMessages converts that row budget into the unit eviction works in,
// through the measured height of the messages this aria actually has: the
// same conversion pageMessages does, and for the same reason: a message is
// anywhere from 4 rows to 400, so a message count is not a memory bound.
func (t *transcript) retainMessages() int {
	avg := t.avgRowsPerMessage()
	if avg <= 0 {
		return transcriptTailLimit
	}
	n := transcriptRetainRows / avg
	if n < transcriptTailLimit {
		n = transcriptTailLimit
	}
	if max := transcriptPageSize * transcriptPageLimit * 4; n > max {
		n = max
	}
	return n
}

// evictStale forgets what has fallen far enough behind the window, and NEVER
// what the window shows. With one owner this is the whole of retention: the
// client's count-based trim is suspended while the pager is up (see enter),
// because trimming the oldest is exactly wrong for a reader who has scrolled
// up: it would drop the page they are looking at.
func (t *transcript) evictStale() {
	floor, ok := t.client.TailFrom(t.retainMessages())
	if !ok {
		return
	}
	if t.from.Less(floor) {
		floor = t.from // never evict inside the window
	}
	t.client.EvictBefore(floor)
}

// tuneTail re-cuts the tail window towards the row budget, using what the last
// frame taught us about how tall this aria's messages are. It grows a window
// that does not fill its budget (or the viewport) and shrinks one that overshot
// it, and reports whether the window changed so the caller can re-materialize.
func (t *transcript) tuneTail() bool {
	if t.tailTuned || !t.follow {
		return false
	}
	_, have := t.heldWindow() // the messages the window holds
	if have == 0 {
		return false
	}
	budget := pageRowBudget()
	fits, rows := t.tailFit(budget)

	// OVERSHOT: the window holds more rows than the budget, and because the
	// walk measured every one of them we know exactly where to cut AND that the
	// message just outside the cut overflows. That is the whole convergence
	// argument: the boundary is a fact about heights we have, not a prediction
	// from an average, so re-deriving it gives the same answer forever.
	if fits < have {
		t.tailWant = fits
		t.tailTuned = true // the cut IS the fixed point; do not re-tune into it
		before := t.from
		t.resetToTail()
		t.tailTuned = true // resetToTail cleared it; the cut still stands
		return t.from != before
	}

	// UNDER BUDGET: the window is everything we hold and it does not fill the
	// budget (or the viewport). Ask for more. The average is used HERE, as a
	// hint for how far to jump, and nowhere else, a wrong hint costs one extra
	// pass, where before it was the control law and could not settle.
	full := rows >= budget || have >= transcriptPageSize
	if full && t.index.total >= t.h {
		t.tailWant = have
		t.tailTuned = true
		return false
	}
	want := have + max(t.pageMessages(), 1)
	before := t.from
	t.tailWant = want
	t.resetToTail() // clears tailTuned; re-derives the floor
	if t.from == before {
		t.tailTuned = true // no messages left to take: this is the whole aria
		return false
	}
	return true
}

// tailFit walks the retained window from the NEWEST committed message
// backwards, adding real measured heights, and reports how many messages fit
// inside budget rows (at least one) and how many rows they are.
func (t *transcript) tailFit(budget int) (messages, rows int) {
	for k := len(t.index.entries) - 1; k >= 0; k-- {
		e := &t.index.entries[k]
		if e.open {
			continue
		}
		h := max(e.height(), 1)
		if messages > 0 && rows+h > budget {
			break
		}
		rows += h
		messages++
	}
	return messages, rows
}

// invalidateWindow is the ONE authority on "the retained window changed". Every
// mutation of the window: the pages, or the store interval's floor: goes
// through it, and it publishes the fact to the line index: windowRev++, which
// buildIndex records, so a moved window always refills lineKey instead of
// relying on the shape diff to notice.
func (t *transcript) invalidateWindow() { t.windowRev++ }

func (t *transcript) pruneCaches() {
	// Called from resetToTail, i.e. once per frame while following the live
	// tail, so the turn set is reused rather than rebuilt.
	keep := t.keepBuf
	if keep == nil {
		keep = make(map[int]bool, transcriptPageSize*transcriptPageLimit)
		t.keepBuf = keep
	} else {
		clear(keep)
	}
	// ROWS FOLLOW THE STORE. A message that has merely fallen out of the WINDOW
	// (the row budget shrinking it back to the tail) is still held by the one
	// owner, and the scroll back up over it must cost neither I/O nor a
	// re-render: which is the whole job the payload LRU used to do.
	t.client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
		keep[m.Turn] = true
		return true
	})
	// The OPEN turn is not in the store's window: it is the live suffix, held
	// separately: so a walk of the window alone does not see it. Without this
	// line its caches were pruned as though it had scrolled out of history:
	// the row cache re-renders and nobody notices, but `expanded` is USER
	// state, and losing it silently undid an expansion the reader had just
	// asked for. It undid it on Escape (which clears the selection through
	// here) and, worse, on every frame while following the live tail: so
	// expanding a streaming tool appeared not to work at all.
	if open := t.openMessage(); open != nil {
		keep[open.Turn] = true
	}
	for k := range t.rowCache {
		if !keep[k.turn()] {
			delete(t.rowCache, k)
		}
	}
	for ref := range t.expanded {
		if !keep[ref.turn] {
			delete(t.expanded, ref)
		}
	}
}

// forEachMessage walks the retained window without materializing a slice of
// it: it is called from the frame path, where one allocation and a copy of
// every retained message header per frame is pure waste.
func (t *transcript) forEachMessage(fn func(aria.Message)) {
	t.client.ForEachIn(t.from, windowEnd, func(m aria.Message) bool {
		fn(m)
		return true
	})
}

// windowEnd is past every real anchor: the high edge of a window that runs to
// the live tail.
var windowEnd = aria.Anchor{Turn: ^uint64(0), Node: ^uint64(0)}

func (t *transcript) messages() []aria.Message {
	out := make([]aria.Message, 0, t.client.Count())
	t.forEachMessage(func(m aria.Message) { out = append(out, m) })
	return out
}

// oldestFrom is the node offset the retained window starts at inside its
// oldest turn. Non-zero means the window holds only the TAIL of that turn, so
// the backward fetch must be anchored on the node: see transcriptPageRequest.
func (t *transcript) oldestFrom() uint64 { return t.from.Node }

// setSize records a new viewport for a transcript that is NOT on screen.
func (t *transcript) setSize(w, h int) {
	if w == t.w && h == t.h {
		return
	}
	t.w, t.h = w, h
	t.prev = nil
	t.invalidateRows()
}

func (t *transcript) resize(w, h int) {
	// Anchor on the message at the viewport top: a width change re-wraps rows and
	// changes line counts, so keeping the raw line offset would jump the view.
	// Record the top message's turn + how many lines into it we are, then restore
	// after re-rendering at the new width. (Skipped when following the tail.)
	anchor, within := t.viewportAnchor()
	t.w, t.h = w, h
	// THE ROW CACHE IS NOT KEYED BY WIDTH. rowCache holds each committed
	// message's rendered rows under (turn, from) alone, and buildIndex below
	// reads it rather than re-rendering: so without this line a width change
	// re-serves rows composed for the OLD width, forever, and the pager paints
	// them into the new viewport.
	t.invalidateRows()
	// Nil means "I know nothing about the screen", and paint honours that by
	// repainting every row: including the blank ones, which is the whole point.
	// It used to claim "full repaint (diff vs nil)" and not get one: paint read a
	// missing base row as "", so every legitimately-blank row compared equal and
	// was skipped, leaving the terminal's own post-resize leftovers in the gaps
	// between nodes. See the comment in paint.
	t.prev = nil
	t.buildIndex() // re-render at the new width, repopulating lineKey
	t.restoreViewportAnchor(anchor, within)
	t.render()
}

// viewportAnchor names the line at the top of the viewport by the SLICE that
// owns it plus how far into that slice it is. Slice, not turn: a page landing
// can prepend an earlier slice of the turn already at the top (the head of one
// clipped by the page budget), and a turn-granular anchor would then restore to
// the head's first line, a jump to the top of a turn the reader was inside.
func (t *transcript) viewportAnchor() (sliceKey, int) {
	if t.follow || t.offset >= len(t.lineKey) {
		return 0, 0
	}
	key := t.lineKey[t.offset]
	start := t.offset
	for start > 0 && t.lineKey[start-1] == key {
		start--
	}
	return key, t.offset - start
}

func (t *transcript) restoreViewportAnchor(key sliceKey, within int) {
	if key == 0 {
		return
	}
	for i, k := range t.lineKey {
		if k == key {
			t.offset = i + within
			return
		}
	}
}

func (t *transcript) invalidateRows() {
	t.rowCache = map[sliceKey]cachedMessage{}
}

// screenMoved voids the painter's model of the terminal: something wrote to
// the screen outside the frame buffer, so t.prev: "the frame the terminal is
// holding": is now fiction, and the next frame must repaint in full.
func (t *transcript) screenMoved() { t.prev = nil }

// resyncDue reports whether the painter owes an unconditional full frame.
func (t *transcript) resyncDue() bool {
	if transcriptResyncInterval <= 0 {
		return false
	}
	if t.now == nil {
		t.now = time.Now
	}
	return t.now().Sub(t.lastFull) >= transcriptResyncInterval
}

// lines renders the retained message window and live tail to physical rows.
// Committed messages are immutable, so their rendered rows are cached by turn;
// only the open message renders every frame.
func (t *transcript) lines() []string {
	t.buildIndex()
	t.lineBuf = t.window(0, t.index.total, t.lineBuf)
	return t.lineBuf
}

// transRule is the memoized message separator: strings.Repeat per message per
// frame is 16 KB/frame of identical strings.
func (t *transcript) transRule() string {
	if t.ruleStr == "" || t.ruleW != t.w {
		t.ruleStr, t.ruleW = dimTransRule(t.w), t.w
	}
	return t.ruleStr
}

// openMessage is the live region: the open turn's suffix, straight from the
// client, EVEN WHEN THE READER HAS SCROLLED AWAY.
func (t *transcript) openMessage() *aria.Message { return t.client.Open() }

// stopFollowing detaches the viewport from the live tail and PINS it where it
// was. Two things must happen before follow drops, because both are only true
// while following:
func (t *transcript) stopFollowing() {
	if !t.follow {
		return
	}
	t.settle()
	t.follow = false
	_, t.offset = t.layout(len(t.footLines()))
}

// settle converges the tail window on the row budget (usually 0-1 passes) -
// the window a following frame would show. Shared by the frame path and by
// stopFollowing, which must not freeze an unconverged window.
func (t *transcript) settle() {
	t.buildIndex()
	for range 3 {
		if !t.tuneTail() {
			break
		}
		t.buildIndex()
	}
}

// footLines is the PIT's body: everything between the transcript's rule and
// the status bar. See pit.go.
func (t *transcript) footLines() []string {
	// The typing boxes are pits too: they used to write themselves into the
	// status row, and take the mantra and the context figure with them.
	rows := t.inputDrawerLines()
	if rows == nil {
		if !t.pit.open() {
			return nil
		}
		t.pit.full = t.fullPit()
		rows = t.pit.lines(t.w, t.pitRoom())
	}
	// FULLSCREEN IS THE PIT AND NOTHING ELSE: no rule, no page position. They
	// describe a conversation that is not on the screen.
	if t.fullPit() {
		return rows
	}
	// The rule opens the region: conversation, rule, pit, blank, bar.
	return append([]string{t.transcriptRule()}, rows...)
}

// transcriptRule is the rule that closes the conversation: the aria, and where
// in it you are.
func (t *transcript) transcriptRule() string {
	total := t.index.total
	body, _ := t.layoutNow()
	pos := ""
	if total > body {
		end := min(t.offset+body, total)
		mark := ""
		if !t.whole() {
			mark = "+"
		}
		pos = fmt.Sprintf("%d–%d/%d%s", t.offset+1, end, total, mark)
	}
	if t.follow {
		pos = strings.TrimSpace(pos + " live")
	}
	return "\x1b[2m" + t.status.ruleLine(t.w, pos) + "\x1b[0m"
}

// pitFocus is who the keys and the screen belong to while a pit is open.
type pitFocus uint8

const (
	focusPit        pitFocus = iota // the default: a pit you opened is a pit you are in
	focusTranscript                 // `T`: read the conversation, keep the pit
)

// fullPit is whether the pit takes the pane right now: the disposition, and
// the focus. A fullscreen pit recedes when the transcript takes the keys.
func (t *transcript) fullPit() bool {
	return t.full && t.pit.open() && t.focused == focusPit
}

// focusTranscriptKey is `T`: hand the screen to the conversation without
// closing what is open, and hand it back on the next press.
func focusTranscriptKey(t *transcript) {
	if !t.pit.open() {
		return
	}
	if t.focused == focusPit {
		t.focused = focusTranscript
		return
	}
	t.focused = focusPit
}

// pitRoom is how many rows the pit may have, and the only place that
// arithmetic is done: the pane, minus the bar (one row, or three when it
// wraps), the blank above it, the rule, and a row of conversation.
func (t *transcript) pitRoom() int {
	if t.fullPit() {
		// FULLSCREEN TAKES THE PANE: everything but the status bar, which is
		// inviolable, and the blank row above it. No rule is drawn, so no row
		// is reserved for one.
		return max(t.h-t.barRows()-1, 1)
	}
	return max(t.h-t.barRows()-5, 1)
}

// layoutNow is layout against the CURRENT stanza height, without re-entering
// footLines (which calls transcriptRule, which would call this).
func (t *transcript) layoutNow() (body, maxOff int) {
	foot := 0
	if rows := t.inputDrawerLines(); rows != nil {
		foot = len(rows)
	} else if t.pit.open() {
		t.pit.full = t.fullPit()
		foot = len(t.pit.lines(t.w, t.pitRoom()))
	}
	return t.layout(foot + 1) // +1 for the transcript rule above the pit
}

// padTo right-pads to n display columns.
func padTo(s string, n int) string {
	if w := runewidth.StringWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// setCmdOut shows a command's output panel.
func (t *transcript) setCmdOut(title string, rows []string) {
	t.queuedByKey = false
	drows := make([]pitRow, 0, len(rows))
	for _, r := range rows {
		// A HEADER IS NOT A ROW YOU CAN ACT ON. Raw command output has no
		// structure to read, so the rule is textual and deliberately crude: a
		// blank line, or a line with no aria-shaped id in it, is chrome. It is
		// better than making the summary line selectable, which is what the
		// first cut did, and worse than a verb that returns rows -- which is
		// the fix, and is the same fix as everything else in the dodge list.
		// SELECTABLE MEANS "HAS AN ID", and yanking gives you the id -- Gluck:
		// "y should work on that row to yank the id of that aria or form". A
		// summary line or a column header has none, so it is chrome and ^N
		// steps over it.
		id := rowID(r)
		drows = append(drows, pitRow{text: r, yank: id, id: id})
	}
	t.pit.showList(pitOutput, ":"+title, drows)
	t.focused = focusPit
	t.render()
}

// layout splits the viewport into the content body and the bottom chrome: the
// rule and the status row always, the open panel when there is one, and: ONLY
// while following: one blank padding row above the rule.
// layout reserves the footer's rows. THE BAR IS NOT ALWAYS ONE ROW: on a
// narrow pane it is three (left, blank, right), and reserving one while
// painting three would push the conversation's last line off the top. The
// rule is one row; barRows is the rest.
func (t *transcript) layout(foot int) (body, maxOff int) {
	body = t.h - 1 - t.barRows() - foot
	if t.follow {
		body--
	}
	if body < 1 {
		body = 1
	}
	return body, max(t.index.total-body, 0)
}

// renderMsgBase renders one message without selection decoration. Committed
// instances are cached; open messages are rebuilt on every live frame.
func (t *transcript) renderMsgBase(m aria.Message) cachedMessage {
	composed := t.composer(m).Message(m, t.w)
	rows := make([]transcriptRow, 0, len(composed))
	for _, r := range composed {
		switch r.Block {
		case ldrender.BlockChrome:
			rows = append(rows, transcriptRow{text: r.Text})
			continue
		}
		ref := nodeRefAt(m, r.Block)
		if r.Block == ldrender.BlockInquiry {
			// The turn's opening question is TEXT ON THE TURN. It occupies no
			// node index, so its rows carry the sentinel ref: that is what
			// makes it select, copy and highlight exactly as a node does, which
			// is how it behaved when it WAS one.
			ref = nodeRef{turn: m.Turn, index: inquiryNode}
		}
		// Rows are stored already clipped (their unselected resting form) so a
		// frame that touches nothing allocates nothing; see plainNodeRow.
		// collapseSGR then strips the rendition churn glamour emits per cell -
		// 3/4 of the retained row text, and of the bytes each painted frame puts
		// on the wire. It is applied here, on the way into the cache, so the
		// saving is paid once and collected on every frame; see sgr.go.
		rows = append(rows, transcriptRow{text: sgrCollapse(plainNodeRow(r.Text, t.w)), ref: ref})
	}
	return cachedMessage{rows: rows}
}

// composer is the pager's composition: the shared shape, plus the two things
// only the pager has: the Ctrl-O coordinate row above each block, and the
// per-block expansion state a gesture toggles.
func (t *transcript) composer(m aria.Message) ldrender.Composer {
	c := ldrender.Composer{
		// The pager is the surface where Enter means something, so its view
		// may open arguments as well as output (see ariaView.gesture).
		View: pagerView(t.view), Header: messageHeader, Rule: t.transRule, Sender: dimSender, Tick: t.tick,
		Expanded: func(block int) bool { return t.expanded[nodeRefAt(m, block)] },
		// Deltas share the node's expansion gesture: Enter on the node opens
		// its collapsed state line along with its output and arguments.
		State: func(block int, deltas map[string]livedoc.FormDelta, w int) []string {
			ref := nodeRefAt(m, block)
			if block == ldrender.BlockInquiry {
				ref = nodeRef{turn: m.Turn, index: inquiryNode}
			}
			return formDeltaLines(deltas, w, t.expanded[ref])
		},
	}
	if t.verbose() {
		// Ctrl-O draws each block's (turn, node, timestamp) above it; see
		// transcript_coords.go. Asked once per message render, not once per row.
		c.Coord = func(block int, n livedoc.Node) string {
			ref := nodeRefAt(m, block)
			if block == ldrender.BlockInquiry {
				ref = nodeRef{turn: m.Turn, index: inquiryNode}
			}
			return term.Dim(coordLabel(ref.turn, ref.index, nodeCoordAt(n)))
		}
	}
	return c
}

func (t *transcript) render() {
	if !t.active {
		return
	}
	// Clamp the low side BEFORE the gate. "render clamps the offset" was the
	// contract every mutation site relied on (scrollBy, key, the selection
	// scroll-into-view all leave it unclamped on purpose); B's gate made the
	// frame skippable, and with it the clamp. That matters because the offset
	// is read off the frame path too: viewportAnchor indexes lineKey with it
	// when a prefetched page lands, and the search wrap-around takes it modulo
	// the row total, and a negative index is a panic, not a wrong pixel.
	if t.offset < 0 {
		t.offset = 0
	}
	if t.batch > 0 || (t.gate != nil && !t.gate()) {
		t.dirty = true
		return
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

// beginBatch/endBatch bracket a run of state changes that must produce ONE
// frame. Every mutation inside still calls render(); render just records that
// the screen is stale and returns. endBatch draws the settled state once.
func (t *transcript) beginBatch() { t.batch++ }

func (t *transcript) endBatch() {
	if t.batch > 0 {
		t.batch--
	}
	if t.batch == 0 && t.dirty {
		t.dirty = false
		t.render() // re-checks the frame-rate gate, which may defer once more
	}
}

// flush paints a deferred frame unconditionally, ignoring the frame-rate gate.
// It is the trailing render: whoever refuses a frame owes a later flush, so
// the final state is always on screen.
func (t *transcript) flush() {
	if !t.active || !t.dirty || t.batch > 0 {
		return
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

// renderFrame is the frame itself: compose the visible window and paint it.
// render() is only the gate in front of it.
func (t *transcript) renderFrame() {
	// The frame ends in a two-row footer (rule, status), plus one padding row
	// while following, so a viewport shorter than that has nowhere to draw and
	// would index screen[-2]. A pane this small cannot show a paged transcript
	// usefully; skip the frame rather than crash, and pick up on the next resize.
	if t.h < 4 {
		return
	}
	// Converge the tail window on the row budget. D drove this off
	// len(t.lines()), a full materialization of the retained window, which is
	// exactly what A deleted from the frame path. settle reads the index
	// instead, which carries the same counts exactly and for free.
	t.settle()
	foot := t.footLines()
	body, maxOff := t.layout(len(foot))
	total := t.index.total
	if t.follow {
		t.offset = maxOff
	}
	if t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
	screen := t.nextScreen()
	// A's windowed accessor: only the ~40 rows about to be painted are
	// materialized, decorated and highlighted. C's primitives make each of
	// those rows cost a slice read.
	t.rowBuf = t.window(t.offset, t.offset+body, t.rowBuf)
	// THE FRAME'S OWN ROW->NODE MAP, recorded here and nowhere else: a click
	// arrives naming a screen row, and the only honest answer to "which node was
	// on that row" is the one taken from the geometry that was actually painted.
	// Re-deriving it at click time would consult an offset that a live token or a
	// tail re-tune may already have moved: the same staleness selectNode's cold
	// path documents for its viewport seed. See transcript_mouse.go.
	t.frameRefs = t.rowRefs(t.offset, t.offset+body, t.frameRefs)
	copy(screen[:body], t.rowBuf)
	// BOTTOM-ALIGNED, and that is the fix for a stray blank line. layout() takes
	// one row off the body while following (the live padding), so writing the
	// stanza from `body` upward left the slack at the BOTTOM -- an empty row
	// between the pit's last entry and its closing rule. The padding belongs
	// above the transcript's rule, where the live region ends; anchoring the
	// stanza to the bottom of the screen puts it there.
	if t.fullPit() {
		// FULLSCREEN OBSCURES. The first cut dimmed the conversation and left
		// it in place, so the pit read as something laid OVER it. Gluck wants
		// the pane: a form at full height is a screen of its own, and the
		// people it is being built for may not be reading the agent's output
		// at all -- may not, one day, be allowed to. A dimmed transcript is
		// still a transcript on the screen.
		for r := range screen {
			if r < t.h-2 {
				screen[r] = ""
			}
		}
	}
	// The row above the rule is left blank while following -- layout reserved
	// it, and is content otherwise. See layout.
	rule, bar := t.footerRows(total, body)
	// A STANZA TALLER THAN THE PANE KEEPS ITS TAIL, rather than not being
	// drawn: the rows nearest the bar are the ones being read.
	start := t.h - 1 - len(bar) - len(foot)
	if start < 0 {
		foot = foot[-start:]
		start = 0
	}
	for k, l := range foot {
		if r := start + k; r >= 0 && r < t.h-len(bar) {
			screen[r] = l
		}
	}
	if t.pit.open() || t.inSearch || t.inJump {
		// NO CLOSING RULE, AND NO HINTS. An open pit used to be fenced top
		// and bottom, with the lower fence carrying "^N/^P select · y yank ·
		// Esc close" -- a second rule and a row of key advice between the list
		// and the status bar. Gluck's design has neither: one rule above the
		// pit, the rows, a blank, then the bar. The keys are in the help
		// panel, which is now scrollable and one keystroke away; printing them
		// under every list is a caption on a photograph of a caption.
		rule = ""
	}
	// The bar occupies the bottom rows and the rule sits directly above it,
	// however many rows that is.
	if top := t.h - 1 - len(bar); top >= 0 {
		screen[top] = rule
		for i, row := range bar {
			if r := top + 1 + i; r >= 0 && r < t.h {
				screen[r] = row
			}
		}
	}
	t.paint(screen)
}

// footerRows is the transcript's two-row footer, shared with the incipit
// bookend so both modes speak one visual language:
// footerRows is the transcript's own rule and THE STATUS BAR.
//
// THE STATUS BAR IS INVIOLABLE. It used to be a contended resource: the search
// box wrote its query there, the command line wrote itself there, and every
// error wrote its text there -- so the mantra, the context percentage and the
// cost vanished exactly when something had gone wrong. All three now live in
// the pit (see pit.go), and this row shows what is always true.
// barRows is how many rows the status bar will take, asked before it is drawn
// so the reservation and the painting cannot disagree.
func (t *transcript) barRows() int {
	if t.status == nil {
		return 1
	}
	return max(t.status.viewOf(t.openPit(), t.status.barVerbose(), time.Now()).height(t.w), 1)
}

func (t *transcript) footerRows(total, body int) (rule string, bar []string) {
	pos := ""
	if total > body {
		end := t.offset + body
		if end > total {
			end = total
		}
		mark := ""
		if !t.whole() {
			mark = "+"
		}
		pos = fmt.Sprintf("%d–%d/%d%s", t.offset+1, end, total, mark)
	}
	// The live marker is the ONLY thing on screen that says which mode you are
	// in, so it does not ride on the range: a transcript that fits the pane is
	// still either following the tail or pinned where you left it.
	if t.follow {
		pos = strings.TrimSpace(pos + " live")
	}
	// ONE FOOTER, shared with the incipit's bookend: see footerStanza.
	stanza := footerStanza(t.status, t.w, pos, t.openPit(), t.status.barVerbose())
	if len(stanza) == 0 {
		return "", []string{""}
	}
	return stanza[0], stanza[1:]
}

// statusPanelLines is the '!' panel: the figaro-status detail above the footer.
func (t *transcript) statusPanelLines() []string {
	rows := t.status.panelLines()
	if max := t.h - 4; len(rows) > max && max > 0 {
		rows = rows[:max]
	}
	for i, r := range rows {
		rows[i] = "\x1b[2m" + clipToWidth(r, t.w) + "\x1b[0m"
	}
	return rows
}

// showQueuedAuto opens or closes the queue pit because the QUEUE changed, not
// because a key was pressed. A pit opened by hand is never auto-closed.
func (t *transcript) showQueuedAuto(on bool) {
	if on {
		if t.showing(pitQueue) {
			t.refreshQueuePit()
			return
		}
		// Dismissed by hand, or a deliberate pit is up: the reader has said
		// what they want on screen. A note is not deliberate.
		if t.queueDismissed || (t.pit.open() && !t.pit.glance()) {
			return
		}
		t.openQueuePit()
		return
	}
	// The queue is empty: the suppressor has nothing left to suppress, so a
	// LATER queue can open the pit again.
	t.queueDismissed = false
	// A pit the user closed is never auto-reopened.
	if !t.queuedByKey && t.showing(pitQueue) {
		t.pit.close()
	}
}

// firstLineTrim returns the first non-empty line of s with surrounding
// whitespace trimmed: the panel needs one line per queued prompt so the
// list stays scannable.
func firstLineTrim(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// openQueueFromKey marks the queued-prompts panel visible and (if wired) kicks
// an async refresh. The stale snapshot renders immediately so the user sees
// something even if the RPC lags; the refresh replaces it in place.
func (t *transcript) openQueueFromKey() {
	t.queuedByKey, t.queueDismissed = true, false
	t.openQueuePit()
	if t.queuedFetch != nil {
		t.queuedFetch()
	}
}

// openQueuePit builds the queue as a SELECTABLE list: `y` yanks a message's
// text and `x` drops it, which is what turns the queue from a thing you watch
// into a thing you can act on.
func (t *transcript) openQueuePit() {
	// NO TITLE. The pit says what it is on the status bar ("𝄚 queue"), and a
	// title row repeated the same word one line above it while costing a row
	// of the conversation.
	t.pit.showList(pitQueue, "", t.queuedPitRows())
	t.focused = focusPit
}

// refreshQueuePit replaces the rows in place, keeping the selection where
// the reader put it: a queue that re-sorts under the cursor on every poll is a
// queue nobody can hit `x` on.
func (t *transcript) refreshQueuePit() {
	sel, had := t.pit.selected()
	keep := ""
	if had {
		keep = sel.id
	}
	t.pit.replaceRows(t.queuedPitRows(), keep)
}

func (t *transcript) queuedPitRows() []pitRow {
	// An empty queue is an empty pit: the bar says the queue is open, and a
	// row saying "(none)" spends a row on what no rows already said.
	if len(t.queued) == 0 {
		return nil
	}
	rows := make([]pitRow, 0, len(t.queued))
	for _, q := range t.queued {
		rows = append(rows, pitRow{
			text: firstLineTrim(q.text), yank: q.text, id: strconv.FormatUint(q.id, 10),
		})
	}
	return rows
}

// helpLines is the '?' panel: the footer grown upward into a key reference,
// drawn above the footer while output keeps streaming past above it. Any key
// wipes it. (Deliberately a bottom panel, not a floating overlay: the terminal
// has exactly one alternate buffer, and compositing a float into every live
// repaint buys nothing over this.)
func (t *transcript) helpLines() []string {
	body := helpBody()
	rows := make([]string, 0, len(body)+5)
	rows = append(rows, "")
	rows = append(rows, body...)
	// The mouse is documented HERE and not in the keymap because it is not a
	// chord: the table is keyed by keystroke, and every invariant it enforces
	// (one help row per binding, openers derived from the rows) is stated in terms
	// of keys. Smuggling a pointer in as a fake chord would buy one help line at
	// the cost of those invariants. The gesture still has to be discoverable,
	// though, an affordance nobody is told about is one nobody uses (trap #6:
	// test the path of someone who does not know the affordance exists).
	rows = append(rows, mouseHelpRows()...)
	if v := helpVersionLine(); v != "" {
		rows = append(rows, "", "  "+v)
	}
	if max := t.h - 4; len(rows) > max && max > 0 { // tiny pane: keep the top of the list
		rows = rows[:max]
	}
	for i, r := range rows {
		rows[i] = "\x1b[2m" + clipToWidth(r, t.w) + "\x1b[0m"
	}
	return rows
}

// nextScreen hands out the frame buffer to compose into. Two are kept and
// swapped: paint retains the composed frame as t.prev for the next diff, so
// the buffer it displaces can be recycled instead of allocating a fresh
// []string of screen height every frame.
func (t *transcript) nextScreen() []string {
	screen := t.screenSpare
	if cap(screen) < t.h {
		screen = make([]string, t.h)
	} else {
		screen = screen[:t.h]
		clear(screen)
	}
	t.screenSpare = nil
	return screen
}

// paint writes the frame diff. Only changed rows are touched, and each is
// emitted through compactRow, which strips the SGR churn and trailing blanks
// the renderers leave behind (see transcript_paint.go): same cells, a
// fraction of the bytes. The scratch buffer is retained across frames so a
// steady scroll allocates nothing here.
func (t *transcript) paint(screen []string) {
	buf := append(t.paintBuf[:0], t.prefix...)
	// THE SWITCH HAS NOW REACHED THE TERMINAL: record it, because leave() is
	// only allowed to switch back if this happened. enter() queues; this is the
	// only place the queue is drained.
	if t.altPending && t.prefix != "" {
		t.altOn, t.altPending = true, false
	}
	t.prefix = ""
	buf = append(buf, "\x1b[?2026h"...)
	base := t.prev
	// A FULL FRAME IS THE ONLY THING THAT CANNOT BE WRONG. base == nil says the
	// painter has no claim on the screen (enter/resize/screenMoved); resyncDue
	// says its claim has gone stale enough that it must be re-earned. Either way
	// the diff below is skipped wholesale rather than trusted row by row: note
	// that this also disables planScroll, whose prediction is a claim about the
	// screen built from exactly the base we have just disowned.
	full := base == nil || t.resyncDue()
	if full {
		base = nil
		if t.now == nil {
			t.now = time.Now
		}
		t.lastFull = t.now()
	} else if plan, ok := t.planScroll(screen); ok {
		buf = appendScroll(buf, plan)
		base = t.predBuf
	}
	for r := 0; r < len(screen); r++ {
		// A ROW WE HAVE NO RECORD OF IS A ROW WE MUST PAINT: it is unknown, not
		// blank. Reading the base as `var old string; if r < len(base) { old =
		// base[r] }` and then comparing made a missing record indistinguishable
		// from a record of an empty row, so every row whose new content is ""
		// compared EQUAL to a base that did not exist and was skipped entirely.
		if r < len(base) && screen[r] == base[r] {
			continue
		}
		var old string
		if r < len(base) {
			old = base[r]
		}
		buf = appendRowUpdate(buf, r, old, screen[r])
	}
	buf = append(buf, "\x1b[?2026l"...)
	_, _ = t.out.Write(buf)
	t.paintBuf = buf
	t.screenSpare, t.prev = t.prev, screen
}

// appendCUP appends "\x1b[<row>;1H" without going through fmt: the profile put
// 9% of paint in fmt.(*pp).doPrintf for a two-digit number.
func appendCUP(dst []byte, row int) []byte {
	dst = append(dst, '\x1b', '[')
	dst = appendUint(dst, row)
	return append(dst, ';', '1', 'H')
}

func appendUint(dst []byte, n int) []byte {
	if n < 0 {
		n = 0
	}
	if n >= 10 {
		dst = appendUint(dst, n/10)
	}
	return append(dst, byte('0'+n%10))
}

// mode reports which input mode the pager is in: the keymap's view of it.
// The order is the dispatch order: the search box owns the keyboard before a
// panel does, and a panel before the plain pager.
// mode is the KEYBOARD's view, and it is now DERIVED from the pit identity
// rather than computed in parallel with it (pitid.go). The boxes keep their
// own buffers; what they no longer keep is a second opinion about what is on
// screen.
func (t *transcript) mode() keyMode {
	if !t.active {
		return modeIncipit
	}
	return t.openPit().keys()
}

// openPit is what is open, as an identity. THE ONE PLACE that reads the
// booleans: everything else asks this.
func (t *transcript) openPit() pitID {
	switch {
	case t.inSearch:
		return pitSearch
	case t.inJump:
		return pitCommand
	case t.pit.open():
		return t.pit.id
	default:
		return pitNothing
	}
}

// key handles one input byte. Transcript is a locked mode: keys only scroll
// or search: it NEVER self-exits. Exit is Ctrl-D / Ctrl-C, handled at the
// input loop. q and Ctrl-T are deliberately inert here.
func (t *transcript) key(b byte) { t.dispatch(keyEvent{b: b}) }

// navMotion drives a logical navigation key. The arrow cluster shares the
// letter keys' motions as a peer: Up and k are one action bound twice -
// rather than by translating itself back into a byte:
func (t *transcript) navMotion(n navKey) { t.dispatch(keyEvent{nav: n}) }

// dispatch runs one keystroke through the keymap (see keymap.go). Three rules
// live here rather than in the table, because each is a property of a MODE
// rather than of any one binding:
func (t *transcript) dispatch(ev keyEvent) {
	switch t.mode() {
	case modeSearch:
		if ev.nav != navNone {
			return
		}
		if act := pagerAct.pager(modeSearch, ev); act != nil {
			act(t)
		} else {
			t.searchLiteral(ev.b)
		}
		t.render()
		return
	case modeJump:
		// THE ARROW CLUSTER IS LIVE HERE, unlike in the search box: Up/Down are
		// history and Home/End are motions, which is what a command line means
		// by them. An arrow with no row is still swallowed rather than allowed
		// to scroll the transcript behind the prompt.
		if ev.nav != navNone {
			if act := pagerAct.pager(modeJump, ev); act != nil {
				act(t)
				t.render()
			}
			return
		}
		if act := pagerAct.pager(modeJump, ev); act != nil {
			act(t)
		} else {
			t.jumpLiteral(ev.b)
		}
		t.render()
		return
	case modePanel:
		if act := pagerAct.pager(modePanel, ev); act != nil {
			act(t)
			t.render()
			return
		}
		// A SELECTABLE PIT KEEPS ITS OWN KEYS. The any-key-dismisses rule is
		// right for help and status -- you glance and move on -- and wrong the
		// moment a pit is a thing you navigate: ^N used to move the
		// selection and dismiss the list it was selecting in, in that order.
		// A HOSTED VERB GETS FIRST REFUSAL on every key -- UNLESS IT HAS A
		// LIST, in which case the pit owns everything a finger does to move
		// and the view keeps only the verbs that are its own. That inversion
		// is the fix for `form show`: the view was taking j/k and running its
		// own cursor against its own window, which disagreed with the pit's,
		// so the highlight appeared to skip rows.
		if t.focused == focusTranscript {
			// THE CONVERSATION HAS THE KEYS until `T` hands them back; Esc
			// still closes the pit.
			if act := pagerAct.pager(modeTranscript, ev); act != nil {
				act(t)
				t.pendG = ev.b == 'g' && !t.pendG
				t.render()
				return
			}
		}
		if t.focused == focusPit && t.pit.live != nil && t.pit.list() != nil {
			if t.pitVerb(ev) {
				// gg is two keys and this branch returns before the epilogue
				// that arms the first one.
				t.pendG = ev.b == 'g' && !t.pendG
				t.render()
				return
			}
		} else if kv, ok := t.pit.live.(cmdkit.KeyView); ok && ev.nav == navNone && kv.Key(ev.b) {
			t.render()
			return
		}
		// EVERY PIT IS NAVIGABLE, not just a selectable one. The help panel
		// was the proof that this mattered: it is the list that tells you how
		// to scroll, and it was the one list you could not scroll -- taller
		// than the pane, it simply lost its bottom. Now j/k, the arrows, u/d
		// and gg/G drive whichever pit is open, moving a cursor where there
		// is one and the window where there is not.
		// A MESSAGE IS A GLANCE, NOT A LIST. It has one row and nothing to
		// scroll, so it keeps the old rule: any key wipes it. Measured -- with
		// motions gated on `pick != nil`, a failed `:999` note survived every
		// keystroke, because the note is a pit too.
		if t.focused == focusPit && t.pit.list() != nil && !t.pit.glance() && pitOwnsKey(ev) {
			if t.pitMotion(ev) {
				// gg IS TWO KEYS, and the arming lives in the dispatcher's
				// epilogue -- which this branch returns before reaching, so
				// `g` in a pit never armed and `gg` never fired. Measured
				// in a pty; the unit tests press keys through a path that
				// happened to arm it anyway.
				t.pendG = ev.b == 'g' && !t.pendG
				t.render()
				return
			}
			if act := pagerAct.pager(modeTranscript, ev); act != nil {
				act(t)
				t.render()
			}
			return
		}
		t.closePanels()
	}
	if act := pagerAct.pager(modeTranscript, ev); act != nil {
		act(t)
	}
	t.pendG = ev.b == 'g' && !t.pendG
	// A jump's report is TRANSIENT, on the same discipline as pendG: it owns the
	// status row until the next key, then the ordinary status line takes the row
	// back. Without this a failed `:999` would eat the mantra/ctx/cost line for
	// the rest of the session. The key that SET the note never reaches here -
	// jumpAccept returns from the modeJump arm above: so the note always gets
	// its frame.
	if !t.inJump {
		t.jumpNote = ""
	}
	t.render()
}

// ---------------------------------------------------------------------------
// The pager's key actions. Each is one row of the keymap (see keymap.go);
// naming them is what lets the table point at behaviour instead of repeating
// it. They mutate state; painting belongs to the dispatcher.
// ---------------------------------------------------------------------------

func pagerLineDown(t *transcript) { t.scroll(1) }
func pagerLineUp(t *transcript)   { t.scrollNotch(-1) }
func pagerHalfDown(t *transcript) { t.scroll(t.h / 2) }
func pagerHalfUp(t *transcript)   { t.scroll(-(t.h / 2)) }

// pagerTail follows the live tail (G, End).
func pagerTail(t *transcript) {
	t.follow = true
	t.resetToTail()
}

// pagerTop goes to the beginning (Home, and the second g).
func pagerTop(t *transcript) {
	t.stopFollowing()
	t.offset = 0
	t.wantTop = !t.atAriaFloor()
}

// pagerPendingTop is the second half of the two-key gg gesture; the first 'g'
// only arms pendG, which the dispatcher's epilogue owns.
func pagerPendingTop(t *transcript) {
	if t.pendG {
		pagerTop(t)
	}
}

func pagerSearchPrompt(t *transcript) { t.inSearch, t.query = true, "" }
func pagerFindNext(t *transcript)     { t.findRepeat(1) }
func pagerFindPrev(t *transcript)     { t.findRepeat(-1) }
func pagerHelpPanel(t *transcript)    { t.openHelpPit() }
func pagerStatusPanel(t *transcript)  { t.openStatusPit() }
func pagerQueuedPanel(t *transcript)  { t.openQueueFromKey() }

// pagerFormPit is 'S': the form, on the same terms as '?' and 'Q'. S for the
// verb's other spelling, `figaro state`.
func pagerFormPit(t *transcript) {
	if strings.HasPrefix(string(t.pit.id), string(pitForm)) {
		t.closePanels()
		return
	}
	if t.openForm != nil {
		t.openForm()
	}
}

// pagerFocusTranscript is 'T'.
func pagerFocusTranscript(t *transcript) { focusTranscriptKey(t) }

// ^N/^P MOVE THE PIT'S SELECTION WHEN ONE IS UP, and the transcript's node
// selection otherwise. The reader's eye is on the list; a key that scrolled the
// conversation behind it would be answering a question nobody asked.
func pagerSelectNext(t *transcript) { t.selectDown(1) }
func pagerSelectPrev(t *transcript) { t.selectDown(-1) }

func (t *transcript) selectDown(dir int) {
	// A PIT WITH A CURSOR OWNS ^N/^P; the transcript's node selection is a
	// different question, and it is not the one being asked while a list is up.
	if p := t.pit.list(); p != nil && p.hasCursor() {
		t.pit.moveSelection(dir)
		return
	}
	t.selectNode(dir, false)
}
func pagerToggleTools(t *transcript) { t.toggleSelectedNodes() }

// pagerClearSelection is Esc in the pager: drop the active selection, and do
// nothing at all when there is none.
func pagerClearSelection(t *transcript) {
	if t.selection.active {
		t.clearSelection()
	}
}

// closePanels shuts the pit.
func (t *transcript) closePanels() {
	if t.showing(pitQueue) {
		t.queueDismissed = true
	}
	t.pit.close()
	t.queuedByKey = false
	t.focused = focusPit // the next pit opens focused; there is nothing to focus now
}

// showing reports whether the named pit is the one that is open.
func (t *transcript) showing(id pitID) bool {
	return t.pit.open() && t.pit.id == id
}

// panelToggleHelp/Status/Queued: a pit's own key closes it, another
// pit's key switches straight over.
func panelToggleHelp(t *transcript) { t.togglePit(pitHelp, t.openHelpPit) }

func panelToggleStatus(t *transcript) { t.togglePit(pitStatus, t.openStatusPit) }

func panelToggleQueued(t *transcript) { t.togglePit(pitQueue, t.openQueueFromKey) }

func (t *transcript) togglePit(id pitID, open func()) {
	was := t.showing(id)
	t.closePanels()
	if !was {
		open()
	}
}

// openHelpPit is '?': the key reference, unselectable.
func (t *transcript) openHelpPit() {
	rows := make([]pitRow, 0, 24)
	for _, l := range helpBody() {
		rows = append(rows, staticRow(l))
	}
	rows = append(rows, staticRow(""))
	for _, l := range mouseHelpRows() {
		rows = append(rows, staticRow(l))
	}
	if v := helpVersionLine(); v != "" {
		rows = append(rows, staticRow(""), staticRow("  "+v))
	}
	t.pit.showList(pitHelp, "", rows)
	t.focused = focusPit
}

// openStatusPit is '!': this figaro's own numbers.
func (t *transcript) openStatusPit() {
	rows := make([]pitRow, 0, 12)
	for _, l := range t.status.panelLines() {
		rows = append(rows, staticRow(l))
	}
	t.pit.showList(pitStatus, "", rows)
	t.focused = focusPit
}

// panelDismiss is Esc with a panel up: close it, and leave the selection
// alone: Esc's other meaning is not reached from here.
func panelDismiss(t *transcript) { t.closePanels() }

// The search sub-mode's actions.

func searchAccept(t *transcript) {
	t.inSearch = false
	t.matchQuery = t.query
	t.find(t.query)
}

func searchCancel(t *transcript) { t.inSearch, t.query = false, "" }

func searchBackspace(t *transcript) {
	if len(t.query) > 0 {
		t.query = t.query[:len(t.query)-1]
	}
}

// searchLiteral is the search box's fallback: a printable key is text, not a
// binding. The one thing the keymap does not decide: "every printable byte"
// is not a set worth enumerating as rows.
func (t *transcript) searchLiteral(b byte) {
	if b >= 0x20 && b < 0x7f {
		t.query += string(b)
	}
}

// find scrolls to the first line at/after the cursor containing q (wrapping).
func (t *transcript) find(q string) {
	if q == "" {
		return
	}
	t.wantTop = false // a search is a deliberate move; see transcript.wantTop
	t.matchQuery = q
	t.settle() // search the converged window: stopFollowing must not move it under us
	total := t.index.total
	if total == 0 {
		return
	}
	for i := 0; i < total; i++ {
		idx := (t.offset + 1 + i) % total
		if searchContains(t.lineAt(idx), q) {
			t.stopFollowing() // pins the offset, so the jump comes after it
			t.offset = idx
			return
		}
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

// findRepeat jumps to the next (delta > 0) or previous (delta < 0) match of
// the persistent matchQuery. Wraps within loaded lines. If nothing matches
// in-window, falls back to the paged-search worker, which walks BACKWARD: the
// window reaches the live tail by construction, so the only history a search
// can page in is older.
func (t *transcript) findRepeat(delta int) {
	if t.matchQuery == "" || delta == 0 {
		return
	}
	t.wantTop = false // a search is a deliberate move; see transcript.wantTop
	q := t.matchQuery
	t.settle()
	total := t.index.total
	if total == 0 {
		return
	}
	start := t.offset + delta
	for i := 0; i < total; i++ {
		idx := ((start+delta*i)%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.stopFollowing() // pins the offset, so the jump comes after it
			t.offset = idx
			return
		}
	}
	// Nothing in the loaded window; page older history in and keep looking.
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

// activeHighlight is what lines() paints as reverse-video match spans:
// the live query while typing, otherwise the last-executed matchQuery.
func (t *transcript) activeHighlight() string {
	if t.inSearch && t.query != "" {
		return t.query
	}
	return t.matchQuery
}

// highlightMatches wraps every visible occurrence of q in reverse-video (SGR
// 7/27). ANSI escapes in row are preserved and step over them for matching,
// so colored/dimmed rows keep their color and pick up the match band on top.
func highlightMatches(row, q string) string {
	if q == "" || row == "" {
		return row
	}
	const hlOn, hlOff = "\x1b[7m", "\x1b[27m"
	if !strings.ContainsRune(row, '\x1b') {
		if !strings.Contains(row, q) {
			return row
		}
		return strings.ReplaceAll(row, q, hlOn+q+hlOff)
	}
	// While a search is active EVERY retained row is run through here on
	// every frame, and almost none of them match. Reject those with an
	// allocation-free scan instead of materializing the stripped row.
	if visibleIndex(row, q) < 0 {
		return row
	}
	// Slow path: strip ANSI to visible, then re-emit with highlights
	// spliced at the visible byte positions of matches.
	var visBuf strings.Builder
	visBuf.Grow(len(row))
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			i = skipANSI(row, i)
			continue
		}
		visBuf.WriteByte(row[i])
		i++
	}
	visible := visBuf.String()
	if !strings.Contains(visible, q) {
		return row
	}
	next := strings.Index(visible, q)
	matchEnd := -1
	vi := 0
	var b strings.Builder
	b.Grow(len(row) + 16)
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			b.WriteString(row[i:j])
			i = j
			continue
		}
		if next >= 0 && vi == next {
			b.WriteString(hlOn)
			matchEnd = vi + len(q)
			after := matchEnd
			if rel := strings.Index(visible[after:], q); rel >= 0 {
				next = after + rel
			} else {
				next = -1
			}
		}
		b.WriteByte(row[i])
		i++
		vi++
		if vi == matchEnd {
			b.WriteString(hlOff)
			matchEnd = -1
		}
	}
	return b.String()
}

// skipANSI advances past a single ANSI escape sequence starting at row[i].
func skipANSI(row string, i int) int {
	if i >= len(row) || row[i] != '\x1b' {
		return i
	}
	if i+1 >= len(row) {
		return len(row)
	}
	if row[i+1] == '[' {
		j := i + 2
		for j < len(row) {
			final := row[j]
			j++
			if final >= 0x40 && final <= 0x7e {
				break
			}
		}
		return j
	}
	return i + 2
}

func (t *transcript) findPage(q string, messages []aria.Message) bool {
	for _, m := range messages {
		if !t.messageMayRenderQuery(m, q) {
			continue
		}
		rows, ok := t.rowCache[keyOf(m)]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[keyOf(m)] = rows
		}
		for _, row := range rows.rows {
			if searchContains(row.searchText(), q) {
				t.buildIndex()
				for i := range t.index.total {
					if t.lineKey[i] != keyOf(m) {
						continue // only this message's lines can carry the hit
					}
					if searchContains(t.lineAt(i), q) {
						t.offset, t.follow = i, false
						return true
					}
				}
				return false
			}
		}
	}
	return false
}

// visibleIndex returns the byte offset in row at which q occurs in row's
// visible text (ANSI escape sequences skipped, and allowed to interrupt the
// match), or -1. It never allocates: the alternative: building the stripped
// row and calling strings.Contains: cost one allocation per row per frame
// for every row on screen while a search was active.
func visibleIndex(row, q string) int {
	if q == "" {
		return 0
	}
	for start := 0; start < len(row); {
		if row[start] == '\x1b' {
			start = skipANSI(row, start)
			continue
		}
		i, j := start, 0
		for j < len(q) && i < len(row) {
			if row[i] == '\x1b' {
				i = skipANSI(row, i)
				continue
			}
			if row[i] != q[j] {
				break
			}
			i++
			j++
		}
		if j == len(q) {
			return start
		}
		start++
	}
	return -1
}

func searchContains(row, q string) bool {
	if !strings.ContainsRune(row, '\x1b') {
		return strings.Contains(row, q)
	}
	return visibleIndex(row, q) >= 0
}

func (t *transcript) messageMayRenderQuery(m aria.Message, q string) bool {
	if strings.Contains(messageHeader(m.Role), q) {
		return true
	}
	verbose := t.verbose()
	if verbose && m.Inquiry != "" &&
		strings.Contains(coordLabel(m.Turn, inquiryNode, 0), q) {
		return true // the question's coordinate row (see transcript_coords.go)
	}
	for i, n := range m.Nodes {
		if markdownMayRenderQuery(n.Markdown, q) || strings.Contains(n.Name, q) ||
			strings.Contains(n.Summary, q) || strings.Contains(n.Output, q) {
			return true
		}
		if verbose && strings.Contains(coordLabel(m.Turn, int(m.From)+i, nodeCoordAt(n)), q) {
			return true // the node's coordinate row
		}
		if n.Type == livedoc.NodeSteering && strings.Contains("↳ input", q) {
			return true
		}
		if n.Type != livedoc.NodeTool {
			continue
		}
		if n.Name == "" && strings.Contains("tool", q) {
			return true
		}
		glyph := "✓✗" + string(livedoc.SpinnerFrames)
		if strings.Contains(glyph, q) {
			return true
		}
		if n.StartedAt != 0 {
			if strings.Contains(toolElapsed(n), q) {
				return true
			}
			if verbose && (strings.Contains("started "+formatToolTime(n.StartedAt), q) ||
				strings.Contains("finished "+formatToolTime(n.FinishedAt), q)) {
				return true
			}
		}
		if verbose {
			for k, v := range n.Args {
				if strings.Contains(fmt.Sprintf("%s=%v", k, v), q) {
					return true
				}
			}
		}
		if !t.expanded[nodeRefAt(m, i)] && n.Output != "" {
			total := 1 + strings.Count(strings.TrimRight(n.Output, "\n"), "\n")
			if total > nodeBashCapDefault &&
				strings.Contains(fmt.Sprintf("last %d of %d lines", nodeBashCapDefault, total), q) {
				return true
			}
		}
	}
	return false
}

func markdownMayRenderQuery(markdown, q string) bool {
	if strings.Contains(markdown, q) || containsIgnoringMarkdown(markdown, q) {
		return true
	}
	at := 0
	ordered := true
	for _, word := range strings.Fields(q) {
		i := strings.Index(markdown[at:], word)
		if i < 0 {
			ordered = false
			break
		}
		at += i + len(word)
	}
	if ordered {
		return true
	}
	return strings.Contains(markdown, "&") && strings.Contains(html.UnescapeString(markdown), q)
}

func containsIgnoringMarkdown(markdown, q string) bool {
	if q == "" {
		return true
	}
	for start := 0; start < len(markdown); start++ {
		qi := 0
		for i := start; i < len(markdown) && qi < len(q); i++ {
			switch markdown[i] {
			case '*', '_', '~', '`', '[', ']', '(', ')':
				continue
			}
			if markdown[i] != q[qi] {
				break
			}
			qi++
		}
		if qi == len(q) {
			return true
		}
	}
	return false
}

// finishSearch ends a paged search that found nothing, putting the reader back
// where they were. The WINDOW is not put back: history the walk paged in is in
// the store now, and the floor it reached is honest: restoring a higher floor
// would throw away work and re-fetch it on the next scroll. Only the viewport
// goes home.
func (t *transcript) finishSearch(found bool) {
	if found || t.search == nil {
		return
	}
	origin := t.search
	t.search = nil
	t.offset = origin.offset
	t.follow = origin.follow
	t.pruneCaches()
}

func (t *transcript) searchingHistory() bool {
	return t.search != nil
}

func dimTransRule(w int) string {
	if w < 3 {
		w = 3
	}
	return "\x1b[2m" + strings.Repeat("─", w) + "\x1b[0m"
}

// gapRow is a hole, drawn as EXACTLY ONE ROW:
func (t *transcript) gapRow(g *aria.Gap) string {
	label := " the rest of this turn is not loaded "
	switch n := g.Turns(); {
	case n == 1:
		label = " 1 turn not loaded "
	case n > 1:
		label = fmt.Sprintf(" %d turns not loaded ", n)
	}
	w := t.w
	if w < 3 {
		w = 3
	}
	if lw := runewidth.StringWidth(label); lw+4 > w {
		return "\x1b[2m" + clipToWidth(label, w) + "\x1b[0m"
	}
	lead := (w - runewidth.StringWidth(label)) / 2
	return "\x1b[2m" + strings.Repeat("─", lead) + label +
		strings.Repeat("─", w-lead-runewidth.StringWidth(label)) + "\x1b[0m"
}

// gapNear is the hole the viewport is close enough to that we should fill it
// before it has to paint: THE PREFETCH DISTANCE, the same
// transcriptPrefetchScreens the window floor uses. A sentinel that is about to
// come on screen is a fetch we are already late for.
func (t *transcript) gapNear() *aria.Gap {
	t.buildIndex() // the window may have moved since the last frame
	body, _ := t.layout(len(t.footLines()))
	lo := t.offset - transcriptPrefetchScreens*t.h
	hi := t.offset + body + transcriptPrefetchScreens*t.h
	for k := range t.index.entries {
		e := &t.index.entries[k]
		if !e.isGap() {
			continue
		}
		if e.start >= lo && e.start <= hi {
			return e.gap
		}
	}
	return nil
}

// holds reports whether the window is missing anything: a hole inside it, or
// history below its floor. It is what stops the footer's total from claiming
// to be the size of the conversation: see footerRows.
func (t *transcript) whole() bool {
	for k := range t.index.entries {
		if t.index.entries[k].isGap() {
			return false
		}
	}
	return t.atAriaFloor()
}

// dropTurnRows invalidates every slice of one turn. Expansion is addressed by
// turn, but a tall turn is several units, so one toggle can touch more than one.
func (t *transcript) dropTurnRows(lt int) {
	t.dropTurnsRows(map[int]struct{}{lt: {}})
}

// dropTurnsRows is the batched form. Callers invalidating a set of turns must
// use it: the cache is keyed by slice, so a per-turn call costs a full scan,
// and scanning once per ref made selection rehydrate quadratic.
func (t *transcript) dropTurnsRows(lts map[int]struct{}) {
	if len(lts) == 0 {
		return
	}
	for k := range t.rowCache {
		if _, ok := lts[k.turn()]; ok {
			delete(t.rowCache, k)
		}
	}
}

// retarget points the pager at a different aria's client. The WINDOW, the row
// cache, the selection and every derived index describe the conversation that
// was on screen a moment ago, so all of them go: what is kept is the reader's
// posture -- the pane, the panels they had open, the verbose toggle -- because
// those are about the READER, not about the aria.
//
// Called with the render lock held, from livelogTurn.retarget.
func (t *transcript) retarget(client *aria.Client, figaroID string, status *sessionStatus) {
	t.client = client
	if status != nil {
		t.status = status
	}
	// The window and everything derived from it.
	t.from = aria.Anchor{}
	t.offset = 0
	t.follow = true
	t.tailTuned, t.tailWant = false, 0
	t.rowCache = map[sliceKey]cachedMessage{}
	t.expanded = map[nodeRef]bool{}
	t.selection = nodeSelection{}
	t.index = lineIndex{}
	t.lineKey = t.lineKey[:0]
	t.frameRefs = t.frameRefs[:0]
	t.invalidateWindow()

	// A walk or a search aimed at the OLD aria's coordinates must not survive
	// into the new one: `:0` still pending when the subject changes would
	// resolve against a conversation nobody asked it about.
	t.jump, t.jumpNote = nil, ""
	t.search = nil
	t.inSearch, t.query, t.matchQuery = false, "", ""
	t.pendG = false

	// The pager is repainted whole rather than diffed: prev describes rows that
	// belong to a conversation that is no longer on screen.
	t.prev = nil
	if t.active {
		t.client.SetClosedLimit(0) // the pager owns retention while it is up
	}
}

// inputDrawerLines renders the search box or the command line, with Tab's
// candidates under it when there are any. Nil when neither box is up.
func (t *transcript) inputDrawerLines() []string {
	var rows []string
	switch {
	case t.inSearch:
		rows = []string{pitGray(clipToWidth("/"+t.query, t.w))}
	case t.inJump:
		// The PROMPT is the editor's, not a constant: while ^R runs it reads
		// `(reverse-i-search)`needle':` exactly as a shell's does, which is the
		// only thing on screen that says the box is in a search at all.
		//
		// THE BOX WRAPS, and it obeys the pit's page like every other list:
		// a long command used to scroll sideways under the prompt, which is
		// the one place a reader cannot check what they typed. Fullscreen is
		// not offered here -- a box is a thing you are typing INTO, not a
		// screen you are reading.
		rows = t.cmdline.wrap(t.cmdline.prompt(":"), t.w, min(pickerRows, max(t.pitRoom(), 1)))
	default:
		return nil
	}
	for _, l := range t.completionLines() {
		rows = append(rows, l)
	}
	if line, own := t.jumpFooter(); own {
		rows = append(rows, pitGray(clipToWidth("  "+line, t.w)))
	}
	return rows
}

// completionMenuRows is how tall the completion menu may get. Fixed, and
// small: the menu lives inside the pit, above an inviolable status bar, and
// a menu that grows with the candidate count is a menu that swallows the
// conversation it is supposed to be helping you talk about.
const completionMenuRows = 2

// completionLines draws the menu: the candidates around the selected one, in
// columns, with a marker for what is out of view on either side. bash shows a
// list and fish highlights a selection; this does both, because the selection
// is what ^N/^P and repeated Tab move.
func (t *transcript) completionLines() []string {
	if len(t.completions) == 0 {
		return nil
	}
	const perRow = 4
	// The window slides to keep the SELECTED candidate visible, so cycling with
	// ^N past the edge scrolls the menu instead of losing the cursor.
	per := perRow * completionMenuRows
	start := 0
	if t.completionIdx >= 0 {
		start = (t.completionIdx / per) * per
	}
	end := min(start+per, len(t.completions))

	var out []string
	for i := start; i < end; i += perRow {
		stop := min(i+perRow, end)
		cells := make([]string, 0, perRow)
		for k := i; k < stop; k++ {
			cell := padTo(t.completions[k], 18)
			if k == t.completionIdx {
				cell = "\x1b[48;5;237m" + cell + "\x1b[49m"
			}
			cells = append(cells, cell)
		}
		out = append(out, pitGray("  "+strings.Join(cells, " ")))
	}
	// One honest line about what is not shown, on either side.
	if start > 0 || end < len(t.completions) {
		note := fmt.Sprintf("  %d–%d of %d", start+1, end, len(t.completions))
		out = append(out, pitGray(clipToWidth(note, t.w)))
	}
	return out
}

// clearCompletions drops the menu: any edit to the line makes it a lie.
func (t *transcript) clearCompletions() {
	t.completions, t.completionIdx, t.completionAt = nil, -1, 0
}

// cycleCompletion is ^N/^P and repeated Tab: move through the candidates and
// put the selected one IN THE LINE, the way bash's menu-complete and fish both
// do. The word being completed is replaced each time, so cycling never
// concatenates candidates onto each other.
func (t *transcript) cycleCompletion(dir int) {
	if len(t.completions) == 0 {
		return
	}
	n := len(t.completions)
	switch {
	case t.completionIdx < 0 && dir > 0:
		t.completionIdx = 0
	case t.completionIdx < 0:
		t.completionIdx = n - 1
	default:
		t.completionIdx = (t.completionIdx + dir + n) % n
	}
	// Replace from where the word started to the cursor.
	for t.cmdline.cursor > t.completionAt {
		t.cmdline.backspace()
	}
	t.cmdline.insert(t.completions[t.completionIdx])
}

// pitVerb runs an itemised live view's OWN verbs -- Enter expands, y yanks --
// against the row the PIT has selected. The view no longer has a cursor to
// disagree about.
func (t *transcript) pitVerb(ev keyEvent) bool {
	iv, ok := t.pit.live.(itemView)
	if !ok {
		return false
	}
	row, has := t.pit.selected()
	switch {
	case ev.b == '\r' || ev.b == '\n':
		if has && row.id != "" {
			iv.Activate(row.id)
		}
		return true
		// 'y' is NOT claimed here: the input loop already yanks a selected pit row
		// (see inputYank), and two owners for one key is how a yank comes to copy
		// one thing and report another.
	}
	_ = row
	_ = has
	return t.pitMotion(ev)
}

// pitMotion runs the shared list motions against the open pit, and
// reports whether it took the key. ONE VOCABULARY: these are the transcript's
// own motions, pointed at the pit, so a reader who can move around a
// conversation can move around a list without learning a second set.
func (t *transcript) pitMotion(ev keyEvent) bool {
	switch {
	// j/k CHOOSE. In a pit with a cursor they move the selection and the
	// window follows it; in one without (help, status) there is nothing to
	// choose, so they move the window and mean the same thing to the hand.
	case ev.b == 'j', ev.nav == navDown, ev.b == 0x0e:
		t.pit.moveSelection(1)
	case ev.b == 'k', ev.nav == navUp, ev.b == 0x10:
		t.pit.moveSelection(-1)
	// e/y READ: one row of the window, selection untouched. vim's ^E/^Y
	// without the modifier, because a pit is a small thing and the chord is
	// spent on selection already.
	case ev.b == 'F':
		// FULLSCREEN IS A DISPOSITION, not a property of this list: it is the
		// pager's, every pit inherits it, and it survives one pit closing and
		// the next opening.
		t.full = !t.full
		t.focused = focusPit
	case ev.b == 'e':
		t.pit.scrollBy(1)
	case ev.b == 'y' && t.pit.list() != nil && !t.pit.list().hasCursor():
		t.pit.scrollBy(-1)
	case ev.b == 'd', ev.nav == navPageDown:
		t.pit.halfPage(1)
	case ev.b == 'u', ev.nav == navPageUp:
		t.pit.halfPage(-1)
	case ev.b == 'G', ev.nav == navEnd:
		t.pit.toBottom()
	case ev.nav == navHome:
		t.pit.toTop()
	case ev.b == 'g':
		// gg, on the transcript's own two-key discipline: the first g arms,
		// the second acts, and pendG is set by the dispatcher's epilogue.
		if t.pendG {
			t.pit.toTop()
		}
	default:
		return false
	}
	return true
}

// pitOwnsKey reports whether an open pit answers this key itself rather
// than being dismissed by it: the motions, the selection keys and the row
// verbs. Everything else still wipes the pit and acts, which is the rule
// that makes a panel a glance rather than a mode.
func pitOwnsKey(ev keyEvent) bool {
	switch {
	case ev.b == 'j', ev.b == 'k', ev.b == 'u', ev.b == 'd', ev.b == 'g', ev.b == 'G', ev.b == 'e', ev.b == 'F':
		return true
	case ev.nav == navPageUp, ev.nav == navPageDown, ev.nav == navHome, ev.nav == navEnd:
		return true
	case ev.nav == navUp || ev.nav == navDown:
		return true
	case ev.b == 0x0e || ev.b == 0x10: // ^N / ^P
		return true
	case ev.b == 'y' || ev.b == 'x':
		return true
	}
	return false
}

// rowID pulls an aria/form id out of a line of command output, so `y` on a
// `:ls` row yanks the ID rather than the whole rendered line.
func rowID(line string) string {
	for _, f := range strings.Fields(line) {
		f = strings.Trim(f, "@·│ \t")
		if len(f) != 8 || rpc.ValidateAriaID(f) != nil {
			continue
		}
		return f
	}
	return ""
}

// showLivePit hosts a live verb in the pit.
func (t *transcript) showLivePit(name string, v cmdkit.LiveView, full bool) {
	t.queuedByKey = false
	t.pit.showLive(pitID(name), v)
	if full {
		t.full = true
	}
	t.focused = focusPit
	t.render()
}

// setCommandNote is how a command reports back into the footer's status row -
// the same row the jump box writes its failures to, because to a reader they
// are the same thing: the last thing I typed, and what came of it.
// setCommandNote is how a verb reports back. A ONE-LINE RESULT IS AN ALERT, NOT
// A PIT: "sent" is news, it is true for a moment, and it should not cost the
// reader the list they were working in. It lands in the bar's first slot --
// left of the pit's glyph and left of the state -- and retires on its own
// after cli.notice_ttl.
//
// That replaces two older behaviours, both of which were the same mistake in
// different clothes: showing a message pit (which REPLACED an open queue,
// so the next poll rebuilt it and an `x` aimed at row two dropped row one),
// and flashing on the closing rule (which no longer exists).
//
// Anything longer than a line is still a pit, because a bar row cannot hold it.
func (t *transcript) setCommandNote(note string) { t.setCommandNoteAt(note, alertInfo) }

func (t *transcript) setCommandNoteAt(note string, level alertLevel) {
	if note == "" {
		t.status.setNotice("")
		if t.showing(pitNote) {
			t.pit.close()
		}
		t.render()
		return
	}
	if !strings.Contains(note, "\n") && displayWidth(note) <= t.w/2 {
		t.status.setNoticeAt(note, level)
		t.render()
		return
	}
	t.pit.showNote(note)
	t.focused = focusPit
	t.render()
}

// noteYank confirms a yank in the bar and never in a pit -- a confirmation
// that replaced the list it was yanking from is how this went wrong twice.
// A long yank is reported by size, because the bar keeps only what fits.
func (t *transcript) noteYank(text string) {
	one := firstLineTrim(text)
	if displayWidth(one) > 24 {
		one = fmt.Sprintf("%d bytes", len(text))
	}
	t.setCommandNote("yanked " + one)
}

// pagerPitDrop is 'x' on a selected row: ask the owner to drop it. The
// transcript does not know what dropping means -- for the queue it is
// `figaro queue rm <id>`, an RPC -- so it hands the id up, exactly as the ':'
// box hands a command line up.
func pagerPitDrop(t *transcript) {
	row, ok := t.pit.selected()
	if !ok || row.id == "" || t.dropRow == nil {
		return
	}
	t.dropRow(string(t.pit.id), row.id)
	t.pit.removeSelected() // optimistic: the refresh confirms it
}
