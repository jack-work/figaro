package cli

import (
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
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
//
// Keys: j/k line, u/d half-page, gg/G top/bottom, / literal search, n/N
// next/prev match, y copy selection (or aria id when nothing is selected),
// ? help panel. Exit is Ctrl-D/Ctrl-C at the input loop. Not safe for
// concurrent use; the caller serializes all entry points.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client
	status *sessionStatus

	active      bool
	showHelp    bool     // '?': the footer grows into a key-reference panel
	showStatus  bool     // '!': the footer grows into the figaro-status panel
	showQueued  bool     // the footer grows into the queued-prompts panel
	queuedByKey bool     // ...because the user pressed 'Q', not because it filled
	queuedRows  []string // pre-rendered by livelogTurn; see queuedPanelLines
	queuedFetch func()   // async refresh of the queued snapshot; set by the input loop
	w, h        int
	tick        int

	prev   []string // last painted screen (the frame the terminal is holding)
	prefix string   // one-shot escapes emitted with the next frame (see enter)
	// altPending/altOn are the alt-screen PAIRING. enter() queues the switch in
	// prefix and sets altPending; the paint that actually emits it sets altOn;
	// leave() writes the exit sequence only when altOn. Without this, a pager
	// that never painted (h < 4, or a frame deferred behind the rate gate) still
	// wrote 2J + ?1049l — to the user's real screen.
	altPending bool
	altOn      bool
	// resync clock: the painter's model of the screen (prev) is only true while
	// figaro is the ONLY writer to the terminal. It is not. See screenMoved and
	// resyncDue — lastFull is when the screen was last painted unconditionally,
	// and now is injectable so the interval is testable without sleeping.
	now      func() time.Time
	lastFull time.Time
	lineKey  []sliceKey // slice owning each line of lines(), for resize anchoring
	offset   int        // top line of the viewport into lines()
	follow   bool       // stick to the bottom on new content
	pendG    bool       // saw one 'g' (for gg)

	// Frame scheduling. render() marks the screen stale and defers when a
	// batch is open (an input burst being drained) or when the frame-rate gate
	// declines; flush() draws the deferred frame. See beginBatch/endBatch.
	batch   int
	dirty   bool
	gate    func() bool // "may I paint now?" — a false answer owes a later flush()
	painted func()      // notified after each painted frame

	inSearch   bool
	query      string
	matchQuery string // persistent query: highlights + n/N target

	// The ':' coordinate jump (transcript_jump.go). inJump/jumpQuery are the
	// command line, exactly as inSearch/query are the search box; jump is a
	// walk in progress; jumpNote is what the footer says about the last one.
	inJump    bool
	jumpQuery string
	jumpNote  string
	jump      *transcriptJump

	// Lazy history paging: the pager opens on the store's tail and pulls older
	// history via keyset ReadBefore only when the viewport comes near the window
	// floor ("like Twitter").
	//
	// THERE IS NO ARMED FLAG. checkOlder/checkNewer/noMoreOlder were one bit per
	// edge standing in for "is there more, and where" — the pile of booleans the
	// range store exists to replace. Whether we want a page is now a pure
	// function of three facts nobody has to remember to set: where the viewport
	// sits, what the store holds below the floor, and what the WIRE has said
	// about the beginning (Client.MoreBefore).
	search *transcriptSearch

	// THE WINDOW IS THE STORE'S OWN TAIL, not a copy of it: the half-open
	// interval [from, ∞), whose floor is the anchor of its oldest message. There
	// is no second copy of anything, in either direction.
	//
	// This is what dissolves the frozen detached tail (docs/range-store.md, bug
	// B). The pager used to snapshot the closed tail into t.pages and freeze the
	// open message beside it (heldOpen), because client.Open() is the open
	// SUFFIX: as Live.From advances, nodes LEAVE the suffix and become closed
	// messages, which a frozen window does not hold — so rendering live would
	// make content vanish, and it rendered stale instead. With one owner those
	// released nodes land in the store's head range, which IS the window, so
	// there is nothing to freeze and nothing to lose. from stays pinned while
	// detached, growth appends at the END of line space, the prefix above is
	// unchanged, and t.offset stays valid — the screen genuinely holds still
	// while the bottom block advances.
	//
	// Scrolling up LOWERS the floor: over history the store already holds it is
	// free (Client.Before), and below that a ReadBefore is merged into the store
	// silently (Client.Merge) and the floor drops onto it. Phase 2a still put
	// fetched history in t.pages, which took the window off the tail and left
	// openMessage nothing to draw; both are gone.
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
	// form — clipped and gutter-prefixed (plainNodeRow), but carrying no
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
	//
	// predBuf/keysNew/keysOld belong to the painter too: they are the
	// scroll-region machinery (the predicted post-shift grid and the row
	// fingerprints that propose the shift), touched only inside planScroll.
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
// Only the VIEWPORT is restored — history the walk paged in stays in the store.
type transcriptSearch struct {
	query  string
	offset int
	follow bool
}

// transcriptPageRequest is one backward read: the keyset cursor to read before,
// and how many messages to ask for. There is no direction any more — the window
// runs to the live tail by construction, so the only history that can be
// missing is OLDER.
type transcriptPageRequest struct {
	before int
	// beforeNode is the node offset of `before`: the oldest retained slice can
	// start MID-TURN (a page clipped at its head), and asking for what precedes
	// the TURN would skip the rest of it forever — its head nodes and the
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
// row — which reads as the status line "eating" the line above it.
//
// The switch RIDES THE FIRST FRAME rather than being written ahead of it. Sent
// on its own it leaves an EMPTY alt screen on the terminal for as long as the
// first frame takes to compose — milliseconds of row rendering, which a 20 ms
// sampler catches blank, and which reads as the conversation blinking out on
// the way into the pager.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query, t.matchQuery = false, false, "", ""
	t.inJump, t.jumpQuery, t.jumpNote, t.jump = false, "", "", nil
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
	// same condition — that is what altPending/altOn record.
	//
	// TWO ESCAPES ARE GONE FROM THIS LINE, both provably redundant:
	//
	//   \x1b[2J   — DECSET 1049 switches to the alternate buffer "clearing it
	//               first" (xterm ctlseqs). On a terminal that honours 1049 the
	//               erase is a no-op; on one that does not, it is the exact
	//               instruction that wipes the user's real screen — the hazard
	//               just removed from leave(), sitting here in its other copy.
	//   autowrapOff + cursorHide
	//             — both entry points already emit them before a pager can
	//               exist (stream.go, listen.go), newLivelogTurn has no third
	//               caller, and nothing between there and here restores either.
	t.prefix = altScreenOn + ldmouse.Enable
	t.altPending = true
	t.render()
}

// leave restores the normal screen — but ONLY if enter() ever actually reached
// it. Mouse reporting is disabled before the alt-screen swap so no stray
// \x1b[<…M leaks into the shell.
//
// THE PAIRING IS THE WHOLE POINT, and it was broken. enter() only QUEUES the
// switch (t.prefix, emitted by the next paint), and a frame is not guaranteed:
// renderFrame returns early under 4 rows, and render() can defer behind the
// frame-rate gate. leave() nonetheless wrote the exit sequence unconditionally
// — so on a terminal we had never switched, \x1b[2J erased THE USER'S OWN
// SCREEN and \x1b[?1049l swapped away from a buffer we were not in. Measured
// in a real pty at 3 rows: ?1049h = 0, ?1049l = 1, 2J = 1.
//
// The irony is that leave() already knew: it clears t.prefix with the comment
// "a frame that never painted must not switch us back" — and then switched
// back anyway. altOn closes that asymmetry.
//
// The erase is gone too, and its absence is deliberate. \x1b[?1049l RESTORES
// the primary screen by definition, so clearing the alt buffer first buys
// nothing on a conforming terminal; on one that does not honour 1049 (conhost
// without VT processing — where the pager's frames land in the primary buffer
// as ordinary text, which is the Windows symptom) it is the one instruction
// that destroys what the user was looking at.
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
// from a stale value — zero on a pager that had not painted a frame yet, which
// is how one 'k' from the inline view landed at the top of a window it had
// never seen.
//
// It no longer ARMS anything. The history prefetch is asked for by geometry
// (pageCursor), so travelling up is not a fact that has to be remembered
// across the frame — it is visible in the offset the gesture leaves behind.
//
// A downward scroll that leaves the viewport past the last row — into the
// live padding — re-attaches. Reaching the last row is not enough: the padding
// is the one row of overscroll you have to ask for, so a reader parked at the
// bottom of a finished turn stays parked. The decision belongs to the GESTURE,
// not to the frame: an offset put out of range by anything else (a search jump,
// a page landing, a resize) is clamped, as it always was.
func (t *transcript) scroll(delta int) {
	t.stopFollowing()
	t.offset += delta
	if _, maxOff := t.layout(len(t.footLines())); t.offset > maxOff {
		pagerTail(t)
	}
}

// scrollNotch is the single-notch gesture (k/Up, one wheel notch). Upward out
// of live, DETACHING IS THE MOTION: stopFollowing hands the live padding row
// back to content, and that row IS the notch of travel spent — the window keeps
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
//
// Message COUNT is a bad unit for a render window: a message is anywhere from
// 4 rows (a one-line answer) to 400 (a tool dump), so a fixed 30-message page
// is a window of 180 rows in one aria and 1400 in the next — and the frame
// costs what the window holds. The geometry is therefore expressed in ROWS:
// transcriptWindowRows is how many rendered rows the pager keeps hot across
// all retained pages, and the per-fetch message count is derived from the
// measured rows-per-message of the aria you are actually reading
// (pageMessages), clamped to [transcriptMinPageSize, transcriptPageSize].
//
// Light arias never reach the row budget, so they keep exactly the old
// 3x30-message geometry. Heavy arias converge to ~8-message pages.
const (
	transcriptPageSize    = 30
	transcriptMinPageSize = 6
	transcriptPageLimit   = 3
	transcriptTailLimit   = 2 * transcriptPageSize
)

// transcriptWindowRows is the retained-window budget in rendered rows. A var,
// not a const, so the geometry sweep in transcript_geometry_bench_test.go can
// measure the tradeoff it encodes. See docs/transcript-paging.md for the
// numbers behind the chosen value.
//
// RE-DERIVED FOR THE MERGED STACK. Axis D measured the knee at 1200 rows
// because back then every retained row cost CPU on every frame (a 1377-row
// window was a 4.22 ms frame, a 300-row window 0.89 ms). Axis A's line index
// removed that: on the merged code a scroll frame is 11-13 µs and a live-follow
// frame 13.6-14.7 µs, FLAT from 600 to 4800 retained rows. The curve that
// picked 1200 no longer exists.
//
// What is left is churn vs retained memory, and the sweep says the crossover is
// set by how deep the user scrolls, not by the budget (journey depth x budget,
// heavy aria — see TestTranscriptGeometryDepthReport):
//
//	depth 120: budget 1200 -> 16 fetches, 0 refetched, 544 renders, 1.9 MB peak
//	           budget 2400 ->  8 fetches, 0 refetched, 612 renders, 2.4 MB peak
//	depth 240: budget 1200 -> 46 fetches, 120 refetched, 1504 renders, 1.9 MB
//	           budget 2400 -> 16 fetches,   0 refetched, 1156 renders, 4.1 MB
//
// 1200 only bought churn-freedom for a journey of ~120 messages, because the
// history the pager retains is the window plus transcriptPayloadLRULimit pages
// of it, i.e. ~5x the budget in rows. 2400 doubles the depth that is free, and
// for deep trips it is cheaper in CPU too. It costs ~2 MB more retained rows and
// ~1 ms more on Ctrl-T (cold enter renders one page: 3.8 ms -> 4.8 ms) — both
// less than ONE pre-merge frame's 4 MB of allocation.
//
// The upper bound is principled rather than arbitrary: at 4800 the derived page
// size saturates the transcriptPageSize ceiling (1600/46 > 30), so the rows-based
// geometry degenerates into exactly the message-count geometry it replaced.
var transcriptWindowRows = 2400

// heldWindow is the EXACT size of the retained window in line space: rendered
// rows (inter-message rules included) and the number of committed messages
// they belong to. It reads axis A's line index, which already computes both as
// a side effect of every frame.
//
// Axis D had to estimate this by averaging len(rows) over the row cache, and
// listed "a per-page row count would make the geometry exact" as future work.
// Two reasons that estimate was worse than it looks, both of which the index
// fixes for free:
//
//   - the row cache is no longer the retained window. D's own change (rows
//     follow payloads into the LRU) means the cache holds up to
//     transcriptPayloadLRULimit extra pages of *history*, so the average was
//     taken over messages the window does not hold and never counted the
//     open message the viewport is actually looking at.
//   - it approximated the separator as a flat +3 per message, including for
//     the first one, which has none.
//
// The open entry is excluded: it is the live message, it changes height every
// token, and the geometry is about how much committed history to keep.
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
	// invisible — the rows it adds are ABOVE a viewport pinned to the tail.
	return transcriptMinPageSize
}

// transcriptPrefetchScreens is how close (in viewports) the scroll position has
// to get to an edge of the retained window before we start pulling the next
// page. One screen means the fetch is armed only once the user is already
// looking at the last rows we have, so the RPC lands *after* they hit the wall;
// two gives a screenful of runway, which at wheel speed is a few hundred
// milliseconds — enough to cover a local daemon round trip.
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
//
// This is the old noMoreOlder bit, un-latched. "Is there older history" is a
// fact only the WIRE knows — a backward read reports it and an empty backward
// read proves it — so it is kept where the wire's answer is (Client.MoreBefore)
// rather than mirrored into a pager boolean that every path moving the window
// has to remember to reset.
//
// Turn 1 node 0 short-circuits it: the head slice of the first turn is the
// oldest thing that can exist, so standing on it needs no round trip to
// confirm. (Only that slice. A window holding the TAIL of turn 1 still has
// that turn's own head to fetch, and the question with it — and a FORKED
// aria's first turn is not 1 at all, which is why the wire's answer is still
// the general case.)
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

// lowerFloor moves the window's floor down onto a. It never raises it — that is
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
	t.finishSearch(false)
	t.jumpAdvance()
	t.render()
}

// pageCursor asks whether the pager wants a page of older history, and from
// where. It is called after every input chunk and again after every landing;
// the answer is derived, not remembered.
//
// FREE HISTORY FIRST. What the store still holds below the floor costs no round
// trip, so the window is extended over it until either the asker is satisfied
// or the store runs out; only then does the wire get asked.
func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	for t.active && t.wantOlder() {
		anchor, within := t.viewportAnchor()
		if gained := t.growWindow(t.pageMessages()); len(gained) > 0 {
			t.absorbOlder(gained, anchor, within)
			continue
		}
		break
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
	// its target may be inside a hole two screens away — jumpReachOf answers
	// "not yet" for exactly that case. Without this branch that answer has
	// nothing to wait for: atAriaFloor below is true (the hole is INSIDE the
	// window, not under it), so no page is requested and the walk stands there
	// saying "jumping to the beginning…" forever. It spends budget like any other
	// fetch, so a hole that refuses to close ends in the honest failure rather
	// than a spin.
	if t.jump != nil {
		if gap := t.oldestGap(); gap != nil {
			if t.jump.fetches <= 0 {
				t.abandonJump(fmt.Sprintf("%s is more than %d pages away — scroll or search for it",
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
			t.abandonJump(fmt.Sprintf("%s is more than %d pages away — scroll or search for it",
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
//
// The order is load-bearing. The anchor is restored BEFORE the jump advances,
// so a landing overwrites the anchor rather than the other way round.
func (t *transcript) absorbOlder(gained []aria.Message, anchor sliceKey, within int) {
	if t.search != nil {
		if t.findPage(t.search.query, gained) {
			t.search = nil
		}
		return
	}
	t.buildIndex()
	t.restoreViewportAnchor(anchor, within)
	t.jumpAdvance()
}

// applyPage folds a fetched page of older history into the ONE owner and drops
// the window's floor onto it.
//
// The page goes through Client.Merge, which is SILENT — it fires no OnClosed,
// whose inline branch would freeze every historical message into the user's
// native scrollback. That silence is why history no longer needs a second home
// (t.pages, deleted): there is nowhere else to put it and no reason to want
// one.
func (t *transcript) applyPage(req transcriptPageRequest, page historyPage) {
	if !t.active {
		return
	}
	if len(page.msgs) == 0 {
		// An empty ReadBefore IS the floor, and it is the only way to find it on
		// a FORKED aria, whose first turn id is not 1.
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
	t.lowerFloor(anchorOf(page.msgs[0]))
	t.absorbOlder(page.msgs, anchor, within)
	if t.search != nil {
		return // still walking; the worker asks for the next page
	}
	t.render()
}

// anchorOf is a message's address — the pair (Turn, From) that identifies it
// everywhere in this codebase.
func anchorOf(m aria.Message) aria.Anchor {
	return aria.Anchor{Turn: uint64(m.Turn), Node: m.From}
}

// historyPage is a fetched page folded into the pager's units, WITH the turn
// extents the wire stated. The extents are what let the store decide that the
// last node of turn t and the first of turn t+1 are neighbours rather than a
// hole: an anchor cannot answer that on its own (docs/range-store.md,
// "Adjacency is NOT decidable from an anchor"), and a page clipped at its tail
// states nothing, so the map is deliberately partial.
type historyPage struct {
	msgs    []aria.Message
	extents map[int]uint64
	// more is the wire's own answer to "is there anything before this page"
	// (Page.More.Before). It is the only honest source for it — an anchor cannot
	// know, and the pager's old noMoreOlder mirrored it into a latch that every
	// window move had to remember to reset.
	more bool
}

// committedPage is committedMessages plus those extents — the fold used
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
//
// A turn is NOT a bounded thing, which is why the slicing exists. Measured on
// the largest real aria (1624 messages, 38 turns): median 119 rendered rows,
// p99 3063, max 4598 — and THREE turns each exceed the entire 2400-row
// retained-window budget on their own. A whole-turn unit would blow the window
// on a single entry and leave no way to scroll into it. Slicing at node
// boundaries restores the 4..400-row unit the row geometry was tuned against.
func committedMessages(p aria.Page) []aria.Message {
	messages := make([]aria.Message, 0, len(p.Parts))
	for _, part := range p.Parts {
		// The inquiry belongs to the slice that STARTS the turn; a part clipped
		// off the head of one must not repeat it. A part with no nodes still
		// carries its question — that is a turn that produced nothing.
		inquiry := ""
		if !part.ClippedHead && part.From == 0 {
			inquiry = part.Inquiry
		}
		if len(part.Nodes) == 0 {
			if inquiry != "" {
				messages = append(messages, aria.Message{
					Turn: int(part.ID), Inquiry: inquiry, Role: livedoc.RoleInput,
				})
			}
			continue
		}
		messages = appendTurnSlices(messages, part.ID, part.From, inquiry, part.Nodes)
	}
	return messages
}

// transcriptUnitChars bounds one pager unit's payload. Characters, not rows,
// so the split is a pure function of the page — no width, no renderer, no
// per-frame cost. It only has to bound the unit, not measure it exactly.
const transcriptUnitChars = 40000

func nodeChars(n livedoc.Node) int {
	return len(n.Markdown) + len(n.Output) + len(n.Summary)
}

// appendTurnSlices cuts a turn into bounded units at node boundaries,
// appending into dst. A node is never split: the smallest unit is one node,
// however large, because tool output is already clamped by composeBashCap.
//
// One cut, at transcriptUnitChars, so one enormous turn still pages. There is
// no voice cut any more: every node is agent output (the inquiry is text on the
// turn, a steer is an inline annotation), so a turn is one voice throughout and
// the unit's single header is always the right one.
//
// The whole-turn case is the overwhelming majority (38 turns -> 41 units on the
// largest real aria) and is allocation-free: this is on the page-refetch path,
// where an intermediate slice per part cost 25-37% of selection rehydrate.
func appendTurnSlices(dst []aria.Message, id uint64, from uint64, inquiry string, nodes []livedoc.Node) []aria.Message {
	unit := func(off int, seg []livedoc.Node) aria.Message {
		m := aria.Message{
			Turn: int(id), From: from + uint64(off), Role: livedoc.RoleOutput, Nodes: seg,
		}
		if m.From == 0 {
			m.Inquiry = inquiry
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
	return appendTurnSlices(nil, id, from, "", nodes)
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
// "rebuilding" it is recomputing one anchor — the floor, tailKeep messages
// back from the end, which Store.TailFrom walks BACKWARD and which therefore
// costs the window's own size, not the aria's.
//
// That is why tailRev is gone. It existed because the rebuild was expensive
// (View() of the whole closed set, a seed merge, a page-descriptor hash and a
// cache scan, per frame), so the pager cached the result and needed the
// client's revision to know when the cache had gone stale. Deriving the floor
// is cheaper than checking whether a copy of it is current — and a copy can
// disagree with the store, which is the disease this phase treats.
func (t *transcript) resetToTail() {
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
// behind the pager's window — the return trip's worth. It plays the part
// transcriptPayloadLRU plays for fetched pages: what the store still holds,
// the row cache still holds rows for, so scrolling back up costs neither I/O
// nor a re-render. Four windows, which is what the LRU was sized at.
var transcriptRetainRows = 4 * transcriptWindowRows

// retainMessages converts that row budget into the unit eviction works in,
// through the measured height of the messages this aria actually has — the
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
// up — it would drop the page they are looking at.
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
//
// Invisible by construction: the tail window is anchored at the newest message
// and the viewport is pinned to the bottom while following, so adding or
// dropping messages at the TOP of the window changes only what is retained.
// Latches once converged, per client revision (a newly committed message
// re-tunes), so a steady stream of frames does no paging work at all.
//
// It reads the line index directly rather than being handed len(t.lines()):
// axis D had only that one number, but the budget and the "is the viewport
// full" check want two different ones. The budget is about retained COMMITTED
// rows; the viewport is about everything on screen, live message included.
// Conflating them let a 400-row streaming reply push the window past
// budget*5/4 and shrink the retained history out from under it.
// withSeed merges the handed-over catch-up page into the tail window. Nothing
// the client already holds is added twice — identity is (Turn, From), the same
// pair everything else in the pager keys on — and the caller's row budget then
// governs the union, so a seed can be trimmed away like any other history.
//
// DELETED at phase 2. The seed existed because a fetched page could not go
// into the client (Apply fires OnClosed, whose inline branch freezes to native
// scrollback), so the pager held a second copy and re-merged it into the tail
// window on every rebuild. aria.Client.Merge folds it into the ONE owner
// silently instead, which is the same page in the same order with no second
// copy and no merge on the frame path. See livelogTurn.enterPager.
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
	// hint for how far to jump, and nowhere else — a wrong hint costs one extra
	// pass, where before it was the control law and could not settle.
	//
	// TWO CEILINGS, because they bound different costs. The ROW budget bounds
	// what a frame and a scroll-back cost, and it is the one the cut above
	// enforces. The MESSAGE ceiling bounds what the per-frame index rebuild
	// costs, which is O(messages) regardless of how short they are — without it
	// an aria of one-line answers would retain thousands of them inside the row
	// budget. Both yield to the viewport: a window that cannot fill the screen
	// is not a window, so an unfilled viewport grows past either.
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
//
// THIS IS THE FIXED POINT the old geometry did not have. tuneTail used to size
// the window as budget/(average height of the window that sizing produced) —
// a controller whose feedback was its own output, damped by a 600/1000-row
// hysteresis band. An average is the wrong instrument the moment the
// distribution has a spike, and this distribution always does: a tool dump is
// 400 rows and an "ok" is 4. One message taller than the band left the loop
// with NO fixed point, so the window alternated between two floors on every
// frame, forever, and the range row in the footer wavered with it
// (1043-1072/1072+ / 546-575/575+, measured 2026-08-01). settle() ran three
// passes per frame, an odd number, so consecutive frames landed on opposite
// phases of the cycle.
//
// A suffix sum of measured heights cannot do that. It is a pure function of
// the heights, so the same window yields the same cut; and the cut is only
// ever taken when the window ALREADY overflows, so the message beyond it has
// been measured too.
//
// The open message is excluded, as it was: the budget governs how much
// committed history to retain, and the live message changes height on every
// token. A zero-height entry still counts as one row, or a run of them would
// let the walk take the whole aria.
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
// mutation of the window — the pages, or the store interval's floor — goes
// through it, and it publishes the fact to the line index: windowRev++, which
// buildIndex records, so a moved window always refills lineKey instead of
// relying on the shape diff to notice.
//
// It used to clear tailRev as well, so the next resetToTail would rebuild the
// pages from the client. There is no snapshot to invalidate any more: the tail
// window is derived from the store on the spot.
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
	// re-render — which is the whole job the payload LRU used to do.
	t.client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
		keep[m.Turn] = true
		return true
	})
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
// it — it is called from the frame path, where one allocation and a copy of
// every retained message header per frame is pure waste.
//
// GAP-BLIND BY CHOICE, which is the contract's default mode: over the store's
// window interval it asks for what is held and is never lied to about
// adjacency — it simply gets less. See docs/range-store.md, "The two verbs".
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
// the backward fetch must be anchored on the node — see transcriptPageRequest.
func (t *transcript) oldestFrom() uint64 { return t.from.Node }

// setSize records a new viewport for a transcript that is NOT on screen.
//
// The pager can be entered long after a resize — the frame path enters it by
// itself the moment the live region grows taller than the viewport — and until
// this existed it entered with the width it was CONSTRUCTED with. A session
// started at 100 columns and resized to 40 painted its first pager frame, and
// every frame after it, at 100: measured at 68 rows past the edge, up to 100
// cells into a 40-column pane, from (*transcript).paint.
//
// So the hidden pager is kept current. No paint, no index rebuild — just the
// size, and the two things that are only true of the old one.
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
	// reads it rather than re-rendering — so without this line a width change
	// re-serves rows composed for the OLD width, forever, and the pager paints
	// them into the new viewport.
	//
	// MEASURED, from inside the process (FIGARO_WIDTH_AUDIT), on one resize from
	// 100 to 40 columns with six seconds of settling first: 67 rows written past
	// the edge, up to 100 cells into a 40-column pane, still going fourteen
	// seconds later. Every one of them came from (*transcript).paint. That is
	// the reported "text beyond the right side that goes away on a rerender" —
	// a later resize happens to rebuild the entries the anchor restore touches.
	//
	// invalidateRows already existed for this shape of problem and was wired
	// only to the verbosity toggle; a width change is the same event.
	t.invalidateRows()
	// Nil means "I know nothing about the screen", and paint honours that by
	// repainting every row — including the blank ones, which is the whole point.
	// It used to claim "full repaint (diff vs nil)" and not get one: paint read a
	// missing base row as "", so every legitimately-blank row compared equal and
	// was skipped, leaving the terminal's own post-resize leftovers in the gaps
	// between nodes. See the comment in paint.
	//
	// Still no \x1b[2J: clearing flickers, and it is not needed once the frame
	// actually covers every row.
	t.prev = nil
	t.buildIndex() // re-render at the new width, repopulating lineKey
	t.restoreViewportAnchor(anchor, within)
	t.render()
}

// viewportAnchor names the line at the top of the viewport by the SLICE that
// owns it plus how far into that slice it is. Slice, not turn: a page landing
// can prepend an earlier slice of the turn already at the top (the head of one
// clipped by the page budget), and a turn-granular anchor would then restore to
// the head's first line — a jump to the top of a turn the reader was inside.
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
// the screen outside the frame buffer, so t.prev — "the frame the terminal is
// holding" — is now fiction, and the next frame must repaint in full.
//
// THE PAINTER IS NOT THE ONLY WRITER. While the pager is up the cursor is
// parked on the last row (the painter finishes every frame there), the alt
// screen has no scrollback, and a stray write of "\n"+text therefore SCROLLS
// THE WHOLE GRID UP ONE ROW and drops text on it. Nothing about that reaches
// the painter: on the next frame every row that composes identically compares
// equal to prev and is skipped, and the rows that do differ are updated from a
// shared-prefix divergence column (appendRowUpdate) — so the update writes a
// TAIL onto a row whose left half now holds scrolled-up content. The visible
// result is a stale status/rule fragment, frozen mid-spinner, sitting to the
// right of unrelated prose, on row after row, for as long as the session
// lasts: measured, and matching the user's report glyph for glyph.
//
// It persisted because nothing invalidated prev. A resize did (resize() nils
// it), which is exactly why resizing or toggling fullscreen "fixed" it by
// hand. This is that repair, made available to the writers that cause the
// damage. Idempotent and free — the cost is one full frame.
func (t *transcript) screenMoved() { t.prev = nil }

// resyncDue reports whether the painter owes an unconditional full frame.
//
// screenMoved covers the writers we know about; this covers the ones we do
// not — a library, the Go runtime, a provider SDK warning, anything holding
// fd 1 or 2. A diff-based painter cannot detect that its model went stale, so
// the only honest guarantee is a bounded one: whatever desynchronizes the
// screen, it is corrected within transcriptResyncInterval rather than
// persisting for the life of the session.
//
// The cost is one full frame per interval AND ONLY WHILE PAINTING — a pager
// sitting idle paints nothing and so resyncs nothing (there is nothing to
// repair: an idle screen no one is writing to cannot drift). A 100x40 frame is
// ~4KB, so at the default interval this is under 1.5 KB/s in the worst case,
// against a live stream that is already far noisier.
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
//
// This is the whole-transcript materialization — O(retained rows). It is NOT on
// the render path any more (that goes through buildIndex + window, which is
// O(viewport)); it survives for the tests and goldens that want to see the
// whole line space at once, and it is the shape the legacyLines oracle mirrors.
//
// The row buffer is reused across calls: the result is only valid until the
// next lines(), which every caller already honours. Nothing else aliases it —
// render() composes through t.rowBuf and paint()'s frame buffers — so keeping
// C's zero-alloc contract here costs A's virtualization nothing.
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
//
// It used to hand back a snapshot frozen at stopFollowing (heldOpen), which is
// what made the detached tail stop advancing until you re-attached. The freeze
// was not laziness: client.Open() is the open SUFFIX, and as Live.From advances
// nodes leave it and become closed messages, so a window that had frozen its
// own copy of the closed set would have watched them disappear. Now the window
// IS the store's tail interval, released nodes land in it, and nothing is lost
// by rendering live.
//
// UNCONDITIONAL since phase 2a-part-2. It used to go quiet once history had
// been paged in, because the fetched page took the window off the tail and
// drawing the live turn beneath a window that no longer reached it would have
// fabricated an adjacency. History lands in the store now and the window keeps
// its head at the tail whatever its floor does, so there is nothing to go
// quiet about — and a selection anchored on the LIVE turn survives paging
// history in, which is what that compromise cost.
func (t *transcript) openMessage() *aria.Message { return t.client.Open() }

// stopFollowing detaches the viewport from the live tail and PINS it where it
// was. Two things must happen before follow drops, because both are only true
// while following:
//
//   - the tail window has to converge (settle). Detaching freezes whatever
//     window is current, and a pager promoted BY a scroll key detaches in the
//     same input chunk that created it — before any frame, so before the
//     catch-up read has been folded into the window. That is why 'k' from the
//     inline view opened on the cold, near-empty window the pager was
//     constructed with, while Ctrl-T (which paints a following frame first)
//     showed the whole tail.
//   - the offset has to be re-derived for the DETACHED geometry, where the
//     live padding row becomes content: the same lines, plus one.
//
// What it no longer does is snapshot the open message. Detaching pins the
// window's FLOOR; its head stays open, so the live turn keeps arriving.
func (t *transcript) stopFollowing() {
	if !t.follow {
		return
	}
	t.settle()
	t.follow = false
	_, t.offset = t.layout(len(t.footLines()))
}

// settle converges the tail window on the row budget (usually 0-1 passes) —
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

// footLines is the open bottom panel, if any: it grows upward from the footer
// and shrinks the body by exactly its height.
func (t *transcript) footLines() []string {
	switch {
	case t.showHelp:
		return t.helpLines()
	case t.showStatus:
		return t.statusPanelLines()
	case t.showQueued:
		return t.queuedPanelLines()
	}
	return nil
}

// layout splits the viewport into the content body and the bottom chrome: the
// rule and the status row always, the open panel when there is one, and — ONLY
// while following — one blank padding row above the rule.
//
// That row is the live affordance, not decoration. While live it is the gap new
// output flows into; once you scroll away it is given back to content, so the
// last row sits flush against the rule and the screen says plainly that it is
// holding still. Scrolling down INTO it is what re-attaches.
func (t *transcript) layout(foot int) (body, maxOff int) {
	body = t.h - 2 - foot
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
//
// The SHAPE of the message — question, rule, header, blocks, blanks — is the
// composer's (ldrender.Composer), shared with the incipit and with `show`. What
// is the pager's own is what it does with the rows: address them, so selection,
// search and the coordinate jump can find a block; store them in their resting
// form; and collapse the rendition churn on the way into the cache.
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
			// node index, so its rows carry the sentinel ref — that is what
			// makes it select, copy and highlight exactly as a node does, which
			// is how it behaved when it WAS one.
			ref = nodeRef{turn: m.Turn, index: inquiryNode}
		}
		// Rows are stored already clipped (their unselected resting form) so a
		// frame that touches nothing allocates nothing; see plainNodeRow.
		// collapseSGR then strips the rendition churn glamour emits per cell —
		// 3/4 of the retained row text, and of the bytes each painted frame puts
		// on the wire. It is applied here, on the way into the cache, so the
		// saving is paid once and collected on every frame; see sgr.go.
		//
		// FUTURE WORK, evaluated and deferred at the B+E merge: E proposed
		// doing the collapse inside the memoized render.Prose instead, so the
		// cost is paid once per (markdown, width) rather than once per row-cache
		// fill, and the live inline path benefits too. Measured on this stack
		// the prize is real — with the Prose memo warm, the collapse is 1.2 ms
		// and ~316 KB of the 3.1 ms it takes to fill the cache for a heavy aria
		// (BenchmarkTranscriptHeavyEnter: 1.86 ms without it, 3.08 ms with).
		// It is deferred because collapseSGR would have to live in
		// internal/render while its proof apparatus (sgr_vt_test.go's VT model)
		// is needed by tests on both sides, so the model wants a third package
		// before the transform can move at all.
		rows = append(rows, transcriptRow{text: collapseSGR(plainNodeRow(r.Text, t.w)), ref: ref})
	}
	return cachedMessage{rows: rows}
}

// composer is the pager's composition: the shared shape, plus the two things
// only the pager has — the Ctrl-O coordinate row above each block, and the
// per-block expansion state a gesture toggles.
func (t *transcript) composer(m aria.Message) ldrender.Composer {
	c := ldrender.Composer{
		// The pager is the surface where Enter means something, so its view
		// may open arguments as well as output (see ariaView.gesture).
		View: pagerView(t.view), Header: messageHeader, Rule: t.transRule, Sender: dimSender, Tick: t.tick,
		Expanded: func(block int) bool { return t.expanded[nodeRefAt(m, block)] },
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
	// is read off the frame path too — viewportAnchor indexes lineKey with it
	// when a prefetched page lands, and the search wrap-around takes it modulo
	// the row total — and a negative index is a panic, not a wrong pixel.
	//
	// Only the low side: it is free, whereas the high clamp needs the row total
	// (and so the index), which is exactly the work the gate exists to skip.
	// Overshooting the bottom is benign — every off-frame reader range-checks
	// the high side — and renderFrame still clamps it when the frame is drawn.
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
//
// This is what a mouse-wheel flick needs: the terminal hands us a burst of
// scroll reports in a single read, and nobody wants to see the twenty-three
// intermediate viewports — they only cost latency, because the frame the user
// is waiting for is the last one.
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
	// len(t.lines()) — a full materialization of the retained window, which is
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
	// tail re-tune may already have moved — the same staleness selectNode's cold
	// path documents for its viewport seed. See transcript_mouse.go.
	t.frameRefs = t.rowRefs(t.offset, t.offset+body, t.frameRefs)
	copy(screen[:body], t.rowBuf)
	for k, l := range foot {
		if r := body + k; r < t.h-2 {
			screen[r] = l
		}
	}
	// The row above the rule (screen[t.h-3]) is left blank while following —
	// layout reserved it — and is content otherwise. See layout.
	rule, status := t.footerRows(total, body)
	screen[t.h-2] = rule
	screen[t.h-1] = status
	t.paint(screen)
}

// footerRows is the transcript's two-row footer, shared with the incipit
// bookend so both modes speak one visual language:
//
//	─────────…──────────────── aria <id> · 48–97/97+ live ───
//	<mantra> · thinking ⠧ · ctx … · cost … · <time> · ? help · ! status
//
// The rule row carries the identity + scroll position right-aligned; the
// status row is plain left-aligned text (fig status at a glance). In search,
// the status row becomes the query prompt.
//
// THE TOTAL IS ROWS WE HOLD, NOT ROWS THAT EXIST, and it never was anything
// else — the pager cannot know how tall an unrendered turn is. Once a hole can
// render, a bare "97" would read as the size of the conversation, so an
// incomplete buffer marks the total with a trailing `+`: "at least this many,
// and there is more we do not hold". MARKED RATHER THAN DROPPED, deliberately:
// the position within what we hold is the number a reader actually navigates
// by, and "48–97" with no total is less useful without being more honest. The
// mark is on whenever the window contains a gap OR history exists below its
// floor.
func (t *transcript) footerRows(total, body int) (rule, status string) {
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
	rule = "\x1b[2m" + t.status.ruleLine(t.w, pos) + "\x1b[0m"
	if t.inSearch {
		return rule, "\x1b[2m" + clipToWidth("/"+t.query, t.w) + "\x1b[0m"
	}
	// The jump owns the status row while it is being typed, while it walks,
	// and to report a target it could not reach — see jumpFooter.
	if line, own := t.jumpFooter(); own {
		return rule, "\x1b[2m" + clipToWidth(line, t.w) + "\x1b[0m"
	}
	return rule, "\x1b[2m" + t.status.statusLine(t.w, true) + "\x1b[0m"
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

// queuedPanelLines is the 'Q' panel: the currently-queued (accepted but not
// yet started) user prompts, oldest first. The rows are a snapshot livelogTurn
// hands down (queuedRows) as figaro.queued reports it. Purely observational —
// there is no cancellation surface here.
// showQueuedAuto opens or closes the panel because the QUEUE changed rather
// than because a key was pressed. A panel the user opened by hand is never
// auto-closed: draining the queue must not yank away a view they asked for.
func (t *transcript) showQueuedAuto(on bool) {
	if on {
		t.showQueued = true
		return
	}
	if !t.queuedByKey {
		t.showQueued = false
	}
}

func (t *transcript) queuedPanelLines() []string {
	// The SAME rows the inline trailer draws (livelogTurn.queuedRows), handed
	// down at set time. One list, one rendering: the pager and incipit
	// disagreeing about how a waiting prompt looks is exactly the live-vs-
	// committed divergence this codebase keeps paying for.
	rows := t.queuedRows
	if len(rows) == 0 {
		rows = []string{"", term.Dim("↳ queued messages"), term.Dim("   (none)")}
	}
	if max := t.h - 4; len(rows) > max && max > 0 {
		rows = rows[:max]
	}
	return rows
}

// firstLineTrim returns the first non-empty line of s with surrounding
// whitespace trimmed — the panel needs one line per queued prompt so the
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

// openQueuedPanel marks the queued-prompts panel visible and (if wired) kicks
// an async refresh. The stale snapshot renders immediately so the user sees
// something even if the RPC lags; the refresh replaces it in place.
func (t *transcript) openQueuedPanel() {
	t.showQueued, t.queuedByKey = true, true
	if t.queuedFetch != nil {
		t.queuedFetch()
	}
}

// helpLines is the '?' panel: the footer grown upward into a key reference,
// drawn above the footer while output keeps streaming past above it. Any key
// wipes it. (Deliberately a bottom panel, not a floating overlay: the terminal
// has exactly one alternate buffer, and compositing a float into every live
// repaint buys nothing over this.)
//
// The rows are GENERATED from the keymap (see keymap.go), so help that says a
// key exists and a key that exists cannot drift apart. This function owns only
// what the table cannot know: the pane's geometry and its dimming.
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
	// though — an affordance nobody is told about is one nobody uses (trap #6:
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
// the renderers leave behind (see transcript_paint.go) — same cells, a
// fraction of the bytes. The scratch buffer is retained across frames so a
// steady scroll allocates nothing here.
//
// When the frame is mostly the previous frame shifted (any scroll), the rows
// are moved with a scroll region instead of being retransmitted; the diff then
// runs against the predicted post-scroll grid, so a mis-detected shift costs
// bytes and never correctness.
//
// The erase-line stays: compactRow trims trailing blanks, so the row no longer
// overwrites what it does not cover.
//
// Buffer ownership (A/C's discipline, unchanged): paintBuf/predBuf/keysNew/
// keysOld are the painter's; the composed frame is retained as t.prev and the
// frame it displaces goes back to screenSpare for the next compose.
func (t *transcript) paint(screen []string) {
	buf := append(t.paintBuf[:0], t.prefix...)
	// THE SWITCH HAS NOW REACHED THE TERMINAL — record it, because leave() is
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
	// the diff below is skipped wholesale rather than trusted row by row — note
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
		// A ROW WE HAVE NO RECORD OF IS A ROW WE MUST PAINT — it is unknown, not
		// blank. Reading the base as `var old string; if r < len(base) { old =
		// base[r] }` and then comparing made a missing record indistinguishable
		// from a record of an empty row, so every row whose new content is ""
		// compared EQUAL to a base that did not exist and was skipped entirely.
		//
		// resize() nils prev precisely to say "the terminal reflowed under me, I
		// know nothing" — and blank rows are everywhere, because entryLine
		// returns "" for row 0 of every message separator. So each separator's
		// blank row kept whatever the terminal had slid into it and stayed wrong
		// until the viewport moved enough to make that row differ from t.prev:
		// "the gaps in between nodes are populated with text that shouldn't be
		// there from some other line... fixed upon return". Measured with a
		// width-only resize, so no terminal row-shift is needed to provoke it.
		//
		// Guarding the compare on `r < len(base)` fixes the short-base case too
		// (a screen taller than the record), and costs nothing on the hot path:
		// when base is prev or predBuf it is always len(screen).
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

// mode reports which input mode the pager is in — the keymap's view of it.
// The order is the dispatch order: the search box owns the keyboard before a
// panel does, and a panel before the plain pager.
func (t *transcript) mode() keyMode {
	switch {
	case !t.active:
		return modeIncipit
	case t.inSearch:
		return modeSearch
	case t.inJump:
		return modeJump
	case t.showHelp || t.showStatus || t.showQueued:
		return modePanel
	default:
		return modeTranscript
	}
}

// key handles one input byte. Transcript is a locked mode: keys only scroll
// or search — it NEVER self-exits. Exit is Ctrl-D / Ctrl-C, handled at the
// input loop. q and Ctrl-T are deliberately inert here.
func (t *transcript) key(b byte) { t.dispatch(keyEvent{b: b}) }

// navMotion drives a logical navigation key. The arrow cluster shares the
// letter keys' motions as a peer — Up and k are one action bound twice —
// rather than by translating itself back into a byte:
//
//	Up/Down         k/j    one line
//	PageUp/PageDown u/d    half a screen
//	Home/End        gg/G   top / bottom
//
// Half a screen (rather than a full one) because the pager's page unit already
// IS the half-page: u/d, the rows-based paging cursor and the prefetch window
// are all sized against it, and there is no full-page motion to route through.
func (t *transcript) navMotion(n navKey) { t.dispatch(keyEvent{nav: n}) }

// dispatch runs one keystroke through the keymap (see keymap.go). Three rules
// live here rather than in the table, because each is a property of a MODE
// rather than of any one binding:
//
//   - the search box swallows the arrow cluster whole (an arrow is not text,
//     and it must not scroll behind the prompt either);
//   - anything the search box has no row for is literal text;
//   - a panel swallows its own keys, and any OTHER key wipes it and then acts
//     normally — which is why panel mode falls through to the transcript rows.
//
// The trailing pendG update is the gg gesture's whole state machine: only 'g'
// arms it, and every other key — bound or not — clears it.
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
		// Identical to the search box, deliberately: an arrow is not text and
		// must not scroll behind the prompt either, and every unbound printable
		// byte is a character of the coordinate.
		if ev.nav != navNone {
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
		t.closePanels()
	}
	if act := pagerAct.pager(modeTranscript, ev); act != nil {
		act(t)
	}
	t.pendG = ev.b == 'g' && !t.pendG
	// A jump's report is TRANSIENT, on the same discipline as pendG: it owns the
	// status row until the next key, then the ordinary status line takes the row
	// back. Without this a failed `:999` would eat the mantra/ctx/cost line for
	// the rest of the session. The key that SET the note never reaches here —
	// jumpAccept returns from the modeJump arm above — so the note always gets
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

// pagerTop jumps to the top of the retained window (Home, and the second g).
func pagerTop(t *transcript) {
	t.stopFollowing()
	t.offset = 0
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
func pagerHelpPanel(t *transcript)    { t.showHelp = true }
func pagerStatusPanel(t *transcript)  { t.showStatus = true }
func pagerQueuedPanel(t *transcript)  { t.openQueuedPanel() }
func pagerSelectNext(t *transcript)   { t.selectNode(1, false) }
func pagerSelectPrev(t *transcript)   { t.selectNode(-1, false) }
func pagerToggleTools(t *transcript)  { t.toggleSelectedNodes() }

// pagerClearSelection is Esc in the pager: drop the active selection, and do
// nothing at all when there is none.
func pagerClearSelection(t *transcript) {
	if t.selection.active {
		t.clearSelection()
	}
}

// closePanels hides all three bottom panels.
func (t *transcript) closePanels() {
	t.showHelp, t.showStatus, t.showQueued = false, false, false
}

// panelToggleHelp/Status/Queued are the panel-mode rows: a panel's own key
// closes it, another panel's key switches straight over.
func panelToggleHelp(t *transcript) {
	was := t.showHelp
	t.closePanels()
	if !was {
		t.showHelp = true
	}
}

func panelToggleStatus(t *transcript) {
	was := t.showStatus
	t.closePanels()
	if !was {
		t.showStatus = true
	}
}

func panelToggleQueued(t *transcript) {
	was := t.showQueued
	t.closePanels()
	if !was {
		t.openQueuedPanel()
	}
}

// panelDismiss is Esc with a panel up: close it, and leave the selection
// alone — Esc's other meaning is not reached from here.
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
// binding. The one thing the keymap does not decide — "every printable byte"
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
// match), or -1. It never allocates: the alternative — building the stripped
// row and calling strings.Contains — cost one allocation per row per frame
// for every row on screen while a search was active.
//
// Candidate starts are always visible bytes: the scan only ever steps one
// byte past a visible byte, and an escape's interior can only follow the ESC
// byte itself, which is always skipped whole.
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
			if strings.Contains(toolDuration(n, time.Now()), q) {
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
// the store now, and the floor it reached is honest — restoring a higher floor
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
//
//	──────── 13 turns not loaded ────────
//
// Not a proportional placeholder. Paging libraries can size a placeholder
// because item height is known; here a row count is only knowable by
// RENDERING, and a hole of twelve turns might be forty rows or four thousand.
// A sized placeholder would be a number we invented, which is exactly the lie
// this design exists to prevent — so the row says what it knows (how many
// TURNS the hole touches, which the anchors do state) and nothing else.
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
// before it has to paint — THE PREFETCH DISTANCE, the same
// transcriptPrefetchScreens the window floor uses. A sentinel that is about to
// come on screen is a fetch we are already late for.
//
// Binding a gap row IS the fetch trigger; this is what "binding" means for a
// row that carries no node: the viewport binds it by coming within a couple of
// screens of it. That is why the sentinel usually never paints.
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
// to be the size of the conversation — see footerRows.
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
