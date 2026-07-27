package cli

import (
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
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
	showHelp    bool // '?': the footer grows into a key-reference panel
	showStatus  bool // '!': the footer grows into the figaro-status panel
	showQueued  bool // 'Q': the footer grows into the queued-prompts panel
	queuedList  []string
	queuedErr   string
	queuedFetch func() // async refresh of the queued snapshot; set by the input loop
	w, h        int
	tick        int

	prev    []string   // last painted screen (the frame the terminal is holding)
	prefix  string     // one-shot escapes emitted with the next frame (see enter)
	lineKey []sliceKey // slice owning each line of lines(), for resize anchoring
	offset  int        // top line of the viewport into lines()
	follow  bool       // stick to the bottom on new content
	pendG   bool       // saw one 'g' (for gg)

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

	// Lazy history paging: the pager opens on the recent window and pulls older
	// messages via keyset ReadBefore only when you scroll near the top ("like
	// Twitter"). checkOlder is armed by an upward scroll; noMoreOlder latches
	// once a fetch comes back empty.
	checkOlder  bool
	checkNewer  bool
	noMoreOlder bool
	pages       []transcriptPage
	newer       []pageDesc
	payloadLRU  []transcriptPage
	search      *transcriptSearch
	committedW  int

	// THE TAIL WINDOW IS THE STORE'S OWN TAIL, not a copy of it. from is its
	// floor — the anchor of its oldest message — and storeWindow says the window
	// is that half-open interval [from, ∞) rather than the retained pages.
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
	// Fetched older history still lands in t.pages (phase 2a-part-2 moves it),
	// and the first page that does drops storeWindow: the window then no longer
	// reaches the tail, so the open message is not ours to draw at all.
	from        aria.Anchor
	storeWindow bool
	// tailTuned latches the one row-budget retune allowed per tail window.
	tailTuned bool
	// tailWant is the tuned tail-window size in messages (0 = not yet tuned).
	tailWant int
	// windowRev is THE authority on "the retained page set changed". Every
	// mutation of t.pages goes through invalidateWindow, which drops the tail
	// snapshot (so resetToTail rebuilds) and bumps this counter (so the line
	// index refills lineKey instead of inferring it from a shape diff). Keeping
	// both facts on one signal is deliberate: with two independent staleness
	// checks the pages and the index can disagree about which window they are
	// describing.
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
	lineBuf     []string     // whole-window rows, valid until the next lines()
	keepBuf     map[int]bool // reused live-turn set for pruneCaches
	paintBuf    []byte       // reused escape-sequence output buffer
	predBuf     []string     // predicted grid after a scroll-region shift
	keysNew     []uint32     // row fingerprints, screen side (shift detection)
	keysOld     []uint32     // row fingerprints, prev side
	screenSpare []string     // the frame buffer displaced by the last paint
}

type transcriptPage struct {
	desc     pageDesc
	messages []aria.Message
}

// pageDesc is sufficient to replay and verify an evicted immutable page.
type pageDesc struct {
	FirstTurn    int
	LastTurn     int
	Count        int
	ReplayBefore int
	LTHash       uint64
}

type transcriptSearch struct {
	query       string
	pages       []transcriptPage
	newer       []pageDesc
	offset      int
	follow      bool
	noMoreOlder bool
	direction   transcriptPageDirection
}

type transcriptPageDirection uint8

const (
	pageOlder transcriptPageDirection = iota + 1
	pageNewer
)

type transcriptPageRequest struct {
	before int
	// beforeNode is the node offset of `before`: the oldest retained slice can
	// start MID-TURN (a page clipped at its head), and asking for what precedes
	// the TURN would skip the rest of it forever — its head nodes and the
	// inquiry drawn above them.
	beforeNode int
	direction  transcriptPageDirection
	expected   pageDesc
	after      int
	watermark  int
	limit      int // messages to fetch; 0 means transcriptPageSize
	cached     []aria.Message
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
	t.storeWindow, t.from = false, aria.Anchor{}
	t.invalidateWindow() // a fresh session always rebuilds the window from the tail
	t.resetToTail()
	t.prefix = altScreenOn + autowrapOff + ldmouse.Enable + cursorHide + "\x1b[2J"
	t.render()
}

// leave restores the normal screen. Mouse reporting is disabled before the
// alt-screen swap so no stray \x1b[<…M leaks into the shell.
func (t *transcript) leave() {
	t.active = false
	t.selection = nodeSelection{} // no selection survives outside the pager
	t.prefix = ""                 // a frame that never painted must not switch us back
	t.client.SetClosedLimit(transcriptTailLimit)
	io.WriteString(t.out, "\x1b[2J"+ldmouse.Disable+altScreenOff)
	t.prev = nil
}

// scroll moves the viewport by delta lines and arms the history prefetch in
// the direction of travel. Detaching comes FIRST: stopFollowing pins the
// offset at the live view's own position, so the motion is relative to what is
// on screen. Moving the offset first instead scrolled from a stale value —
// zero on a pager that had not painted a frame yet, which is how one 'k' from
// the inline view landed at the top of a window it had never seen.
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
		return
	}
	if delta < 0 {
		t.checkOlder = true // scrolled up: maybe page older history
	} else if delta > 0 {
		t.checkNewer = true
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
		t.checkOlder = true // still travelling up: page older history as before
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
	transcriptDescLimit   = 64
)

// transcriptPayloadLRULimit is how many evicted pages keep their payload (and,
// since rows follow payloads, their rendered rows) for the return trip. Four
// windows' worth: rows-based pages are small, so retaining the same amount of
// history as the old 30-message geometry takes proportionally more of them.
// Measured on the 120-message round trip (see docs/transcript-paging.md):
// 3 pages costs 25 fetches / 72 refetched messages / 920 re-renders, 12 pages
// costs 16 / 0 / 632, and 24 pages buys almost nothing more.
var transcriptPayloadLRULimit = 4 * transcriptPageLimit

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
	if t.avgRowsPerMessage() == 0 {
		return transcriptMinPageSize
	}
	return t.pageMessages()
}

// transcriptPrefetchScreens is how close (in viewports) the scroll position has
// to get to an edge of the retained window before we start pulling the next
// page. One screen means the fetch is armed only once the user is already
// looking at the last rows we have, so the RPC lands *after* they hit the wall;
// two gives a screenful of runway, which at wheel speed is a few hundred
// milliseconds — enough to cover a local daemon round trip.
const transcriptPrefetchScreens = 2

func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	// A jump left waiting on a fetch that never landed (an RPC error clears both
	// edge flags) resumes here, on the next key that asks for a page. The budget
	// is spent by jumpAdvance, so a store that never gets there still stops.
	if t.jump != nil && !t.checkOlder && !t.checkNewer {
		t.jumpAdvance()
	}
	if t.checkOlder && t.noMoreOlder {
		t.checkOlder = false
	}
	if t.checkOlder && !t.noMoreOlder {
		t.checkOlder = false
		if t.search == nil && t.jump == nil && t.offset >= transcriptPrefetchScreens*t.h {
			return transcriptPageRequest{}, false
		}
		oldest, ok := t.oldestLT()
		if !ok {
			return transcriptPageRequest{}, false
		}
		node := t.oldestFrom()
		if oldest <= 1 && node == 0 {
			t.noMoreOlder = true
			t.finishSearch(false)
			// The floor is now known, which is exactly what `:0` was waiting for.
			t.jumpAdvance()
			t.render()
			return transcriptPageRequest{}, false
		}
		return transcriptPageRequest{
			before: oldest, beforeNode: int(node),
			direction: pageOlder, limit: t.pageMessages(),
		}, true
	}
	if t.checkNewer && len(t.newer) > 0 {
		t.checkNewer = false
		// t.index.total, not len(t.lineKey): lineKey is only refilled when the
		// index shape moves, so reading its length here would be a second,
		// weaker way of asking how big line space is.
		if t.search == nil && t.jump == nil && t.offset+transcriptPrefetchScreens*t.h < t.index.total {
			return transcriptPageRequest{}, false
		}
		desc := t.newer[len(t.newer)-1]
		return transcriptPageRequest{
			before: desc.ReplayBefore, direction: pageNewer, expected: desc,
			cached: t.takePayload(desc),
		}, true
	}
	if t.checkNewer {
		t.checkNewer = false
		newest, ok := t.newestLT()
		if ok && newest < t.committedW {
			return transcriptPageRequest{
				direction: pageNewer, after: newest, watermark: t.committedW,
				limit: t.pageMessages(),
			}, true
		}
	}
	return transcriptPageRequest{}, false
}

func (t *transcript) applyPage(req transcriptPageRequest, messages []aria.Message) {
	if !t.active {
		return
	}
	if len(messages) == 0 {
		if req.direction == pageOlder {
			t.noMoreOlder = true
			t.finishSearch(false)
			// An empty ReadBefore IS the floor, and it is the only way to find
			// it on a FORKED aria, whose first turn id is not 1. `:0` resolves
			// here for those.
			t.jumpAdvance()
			t.render()
		} else if t.search != nil {
			t.wrapSearchOlder()
		} else if t.jump != nil {
			t.abandonJump(t.jump.target.missing())
			t.render()
		}
		return
	}
	desc := describePage(messages)
	if req.expected.Count != 0 && !req.expected.equal(desc) {
		t.newer = nil
		t.checkNewer = true
		t.render()
		return
	}
	// THE FIRST PAGE OF OLDER HISTORY takes the window off the store's tail.
	// Materialize what the interval currently holds as the first retained page,
	// so everything below here is the paging code exactly as it was; the window
	// no longer reaches the live turn, which is why openMessage goes quiet.
	// (Phase 2a-part-2 replaces this with a merge into the store and a lowered
	// floor — at which point t.pages goes away entirely.)
	if t.storeWindow {
		if held := t.messages(); len(held) > 0 {
			t.pages = []transcriptPage{{desc: describePage(held), messages: held}}
		}
		t.storeWindow = false
	}
	searching := t.search != nil
	anchor, within := sliceKey(0), 0
	if !searching {
		anchor, within = t.viewportAnchor()
	}
	page := transcriptPage{desc: desc, messages: messages}
	t.invalidateWindow()
	switch req.direction {
	case pageOlder:
		t.pages = append([]transcriptPage{page}, t.pages...)
	case pageNewer:
		t.pages = append(t.pages, page)
		if req.expected.Count != 0 && len(t.newer) > 0 {
			t.newer = t.newer[:len(t.newer)-1]
		}
	}
	t.trimPages(req.direction)
	if searching {
		if t.findPage(t.search.query, messages) {
			t.search = nil
		} else if t.search.direction == pageNewer {
			if t.hasNewerHistory() {
				t.checkNewer = true
			} else {
				t.wrapSearchOlder()
			}
		} else {
			t.checkOlder = true
		}
		if t.search != nil {
			return
		}
	} else {
		t.buildIndex()
		t.restoreViewportAnchor(anchor, within)
		// A jump in flight resolves against the window it just grew, or asks
		// for one more page. It runs AFTER the anchor is restored so that a
		// landing overwrites the anchor rather than the other way round.
		t.jumpAdvance()
	}
	t.render()
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
}

// committedPage is committedMessages plus those extents — the fold used
// wherever the result is going into the store rather than straight to a
// renderer.
func committedPage(p aria.Page) historyPage {
	out := historyPage{msgs: committedMessages(p)}
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

// turnVoice is the voice a unit renders under. Every node is agent output, so
// a unit with nodes is the agent's; one without is an inquiry whose turn
// produced nothing.
func turnVoice(nodes []livedoc.Node) string {
	if len(nodes) == 0 {
		return livedoc.RoleInput
	}
	return livedoc.RoleOutput
}

func (t *transcript) trimPages(direction transcriptPageDirection) {
	if len(t.pages) > transcriptPageLimit {
		t.invalidateWindow() // dropping a page is a window change
	}
	for len(t.pages) > transcriptPageLimit {
		drop := 0
		if direction == pageOlder {
			drop = len(t.pages) - 1
		}
		page := t.pages[drop]
		if direction == pageOlder {
			t.newer = append(t.newer, page.desc)
			if len(t.newer) > transcriptDescLimit {
				copy(t.newer, t.newer[len(t.newer)-transcriptDescLimit:])
				t.newer = t.newer[:transcriptDescLimit]
			}
		}
		t.rememberPayload(page)
		t.dropPage(page)
		copy(t.pages[drop:], t.pages[drop+1:])
		t.pages[len(t.pages)-1] = transcriptPage{}
		t.pages = t.pages[:len(t.pages)-1]
		if direction == pageNewer {
			t.noMoreOlder = false
		}
	}
}

// dropPage releases the caches of a page leaving the retained window. Rows of
// messages whose payload is still held in the LRU are KEPT: that page is one
// scroll-turn away from coming back (the LRU exists precisely so the return
// trip costs no I/O), and re-rendering its prose is far more expensive than
// the rows are to hold. Expansion state rides along with the rows, so the two
// never disagree. Rows are released for real when the payload leaves the LRU.
func (t *transcript) dropPage(page transcriptPage) {
	if page.desc.Count == 0 {
		return
	}
	kept := t.payloadLTs()
	for _, m := range page.messages {
		if kept[m.Turn] {
			continue
		}
		delete(t.rowCache, keyOf(m))
		for ref := range t.expanded {
			if ref.turn == m.Turn {
				delete(t.expanded, ref)
			}
		}
	}
}

// payloadLTs is the set of message LTs whose payload the LRU still holds.
func (t *transcript) payloadLTs() map[int]bool {
	n := 0
	for _, page := range t.payloadLRU {
		n += len(page.messages)
	}
	if n == 0 {
		return nil
	}
	out := make(map[int]bool, n)
	for _, page := range t.payloadLRU {
		for _, m := range page.messages {
			out[m.Turn] = true
		}
	}
	return out
}

func (t *transcript) rememberPayload(page transcriptPage) {
	if page.desc.Count == 0 || transcriptPayloadLRULimit == 0 {
		return
	}
	for i := range t.payloadLRU {
		if t.payloadLRU[i].desc.equal(page.desc) {
			copy(t.payloadLRU[i:], t.payloadLRU[i+1:])
			t.payloadLRU[len(t.payloadLRU)-1] = transcriptPage{}
			t.payloadLRU = t.payloadLRU[:len(t.payloadLRU)-1]
			break
		}
	}
	t.payloadLRU = append(t.payloadLRU, page)
	if len(t.payloadLRU) > transcriptPayloadLRULimit {
		evicted := append([]transcriptPage(nil), t.payloadLRU[:len(t.payloadLRU)-transcriptPayloadLRULimit]...)
		copy(t.payloadLRU, t.payloadLRU[len(t.payloadLRU)-transcriptPayloadLRULimit:])
		clear(t.payloadLRU[transcriptPayloadLRULimit:])
		t.payloadLRU = t.payloadLRU[:transcriptPayloadLRULimit]
		for _, page := range evicted { // rows outlive the window, not the LRU
			t.dropPage(page)
		}
	}
}

func (t *transcript) takePayload(desc pageDesc) []aria.Message {
	for i := len(t.payloadLRU) - 1; i >= 0; i-- {
		if t.payloadLRU[i].desc.equal(desc) {
			messages := t.payloadLRU[i].messages
			copy(t.payloadLRU[i:], t.payloadLRU[i+1:])
			t.payloadLRU[len(t.payloadLRU)-1] = transcriptPage{}
			t.payloadLRU = t.payloadLRU[:len(t.payloadLRU)-1]
			return messages
		}
	}
	return nil
}

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
	if t.storeWindow && t.from == from {
		t.checkNewer = false // the window already IS the tail at this floor
		return
	}
	t.pages, t.newer, t.payloadLRU = nil, nil, nil
	t.from, t.storeWindow = from, true
	t.checkNewer = false
	// The committed watermark is the newest thing the store holds, which is one
	// backward step — no walk, and no dependence on OnClosed having been wired.
	if last, ok := t.client.TailFrom(1); ok && int(last.Turn) > t.committedW {
		t.committedW = int(last.Turn)
	}
	// Turn 1 is the oldest turn there is — but only its HEAD slice is the oldest
	// thing there is. A window holding just the tail of it still has that turn's
	// own head to fetch, and the question with it.
	t.noMoreOlder = n > 0 && from.Turn <= 1 && from.Node == 0
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
	if t.tailTuned || !t.follow || !t.storeWindow {
		return false
	}
	held, have := t.heldWindow() // committed rows + the messages they belong to
	if have == 0 {
		return false
	}
	total := t.index.total // + the open message: what the viewport shows
	want, budget := t.pageMessages(), pageRowBudget()
	if have > 0 && held > 0 && total < t.h { // never leave the viewport half-empty
		perMsg := max(held/have, 1)
		if need := (t.h + perMsg - 1) / perMsg; need > want {
			want = need
		}
	}
	grow := want > have && (held < budget*3/4 || total < t.h)
	shrink := want < have && held > budget*5/4
	if !grow && !shrink {
		// CONVERGED — and the size we converged at is now the window's, latched.
		// The window is derived from tailKeep() on every frame rather than held
		// as a snapshot, so leaving tailWant unset would let the size drift with
		// pageMessages()'s own input (the measured rows-per-message, which the
		// window itself determines). Latching makes the fixed point explicit.
		t.tailWant = have
		t.tailTuned = true
		return false
	}
	before := t.from
	t.tailWant = want
	t.resetToTail() // clears tailTuned; re-derives the floor
	if t.from == before {
		t.tailTuned = true // no messages available to move: stop trying
		return false
	}
	return true
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
	t.forEachMessage(func(m aria.Message) { keep[m.Turn] = true })
	if t.storeWindow {
		// Rows follow payloads: the STORE is what holds a message now, so a
		// message that has merely fallen out of the tail window (the row budget
		// shrinking it) keeps its rendered rows for the scroll back up. That is
		// the job transcriptPayloadLRU does for fetched pages.
		t.client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
			keep[m.Turn] = true
			return true
		})
	}
	for _, page := range t.payloadLRU { // payload retained => rows retained
		for _, m := range page.messages {
			keep[m.Turn] = true
		}
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

// forEachMessage walks the retained window without materializing the merged
// slice messages() returns — it is called from the frame path, where one
// allocation and a copy of every retained message header per frame is pure
// waste.
//
// GAP-BLIND BY CHOICE, which is the contract's default mode: over the store's
// tail interval it asks for what is held and is never lied to about
// adjacency — it simply gets less. See docs/range-store.md, "The two verbs".
func (t *transcript) forEachMessage(fn func(aria.Message)) {
	if t.storeWindow {
		t.client.ForEachIn(t.from, windowEnd, func(m aria.Message) bool {
			fn(m)
			return true
		})
		return
	}
	for _, page := range t.pages {
		for _, m := range page.messages {
			fn(m)
		}
	}
}

// windowEnd is past every real anchor: the high edge of a window that runs to
// the live tail.
var windowEnd = aria.Anchor{Turn: ^uint64(0), Node: ^uint64(0)}

func (t *transcript) messages() []aria.Message {
	var out []aria.Message
	if t.storeWindow {
		out = make([]aria.Message, 0, t.client.Count())
	} else {
		n := 0
		for _, page := range t.pages {
			n += len(page.messages)
		}
		out = make([]aria.Message, 0, n)
	}
	t.forEachMessage(func(m aria.Message) { out = append(out, m) })
	return out
}

func (t *transcript) oldestLT() (int, bool) {
	if t.storeWindow {
		if t.client.Count() == 0 {
			return 0, false
		}
		return int(t.from.Turn), true
	}
	for _, page := range t.pages {
		if len(page.messages) > 0 {
			return page.messages[0].Turn, true
		}
	}
	return 0, false
}

// oldestFrom is the node offset the retained window starts at inside its oldest
// turn. Non-zero means the window holds only the TAIL of that turn, so the
// backward fetch must be anchored on the node — see transcriptPageRequest.
func (t *transcript) oldestFrom() uint64 {
	if t.storeWindow {
		return t.from.Node
	}
	for _, page := range t.pages {
		if len(page.messages) > 0 {
			return page.messages[0].From
		}
	}
	return 0
}

func (t *transcript) newestLT() (int, bool) {
	if t.storeWindow {
		last, ok := 0, false
		t.forEachMessage(func(m aria.Message) { last, ok = m.Turn, true })
		return last, ok
	}
	for i := len(t.pages) - 1; i >= 0; i-- {
		if n := len(t.pages[i].messages); n > 0 {
			return t.pages[i].messages[n-1].Turn, true
		}
	}
	return 0, false
}

// hasNewerHistory: while the window is the store's tail it runs to the live
// turn by construction, so there is nothing newer to page in — the question is
// only meaningful once history has been paged in beneath it.
func (t *transcript) hasNewerHistory() bool {
	if t.storeWindow {
		return false
	}
	if len(t.newer) > 0 {
		return true
	}
	newest, ok := t.newestLT()
	return ok && newest < t.committedW
}

func (t *transcript) observeCommitted(m aria.Message) {
	if m.Turn > t.committedW {
		t.committedW = m.Turn
	}
}

func describePage(messages []aria.Message) pageDesc {
	if len(messages) == 0 {
		return pageDesc{}
	}
	h := fnv.New64a()
	var b [8]byte
	for _, m := range messages {
		v := uint64(m.Turn)
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	last := messages[len(messages)-1].Turn
	return pageDesc{
		FirstTurn: messages[0].Turn, LastTurn: last, Count: len(messages),
		ReplayBefore: last + 1, LTHash: h.Sum64(),
	}
}

func (d pageDesc) equal(other pageDesc) bool {
	return d.FirstTurn == other.FirstTurn && d.LastTurn == other.LastTurn &&
		d.Count == other.Count && d.ReplayBefore == other.ReplayBefore &&
		d.LTHash == other.LTHash
}

func (t *transcript) resize(w, h int) {
	// Anchor on the message at the viewport top: a width change re-wraps rows and
	// changes line counts, so keeping the raw line offset would jump the view.
	// Record the top message's turn + how many lines into it we are, then restore
	// after re-rendering at the new width. (Skipped when following the tail.)
	anchor, within := t.viewportAnchor()
	t.w, t.h = w, h
	t.prev = nil   // full repaint (diff vs nil); no \x1b[2J, which flickers
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
// nil once the window has been paged away from the tail: the open turn is then
// simply not inside it, and drawing it would fabricate an adjacency between
// history and the live turn.
func (t *transcript) openMessage() *aria.Message {
	if !t.storeWindow {
		return nil
	}
	return t.client.Open()
}

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
func (t *transcript) renderMsgBase(m aria.Message) cachedMessage {
	var rows []transcriptRow
	// Ctrl-O draws each node's (turn, node, timestamp) above it; see
	// transcript_coords.go. Asked once per message render, not once per row.
	coords := t.verbose()
	// The turn's opening question is TEXT ON THE TURN, carried by its first
	// slice only. It occupies no node index, so its rows carry the sentinel ref
	// (see inquiryNode) — that is what makes it select, copy and highlight
	// exactly as a node does, which is how it behaved when it WAS one.
	if iq := inquiryProse(m.Inquiry, t.w-2); len(iq) > 0 {
		ref := nodeRef{turn: m.Turn, index: inquiryNode}
		rows = append(rows, transcriptRow{text: messageHeader(livedoc.RoleInput)}, transcriptRow{})
		if coords {
			// No timestamp: the question's arrival time lives on aria.Turn.At,
			// and aria.Message — the pager's unit — does not carry it. The
			// ADDRESS is what the jump needs, and the address is what we have.
			rows = append(rows, t.coordRow(ref, 0))
		}
		for _, l := range iq {
			rows = append(rows, transcriptRow{text: collapseSGR(plainNodeRow(l, t.w)), ref: ref})
		}
		// A blank and the RULE close the question before the agent speaks. The
		// question used to be its own message and got that rule as the message
		// separator; the two voices now share one message, so it is drawn here.
		if len(m.Nodes) > 0 {
			rows = append(rows, transcriptRow{}, transcriptRow{text: t.transRule()})
		}
	}
	if h := messageHeader(m.Role); h != "" && len(m.Nodes) > 0 {
		rows = append(rows, transcriptRow{text: h}, transcriptRow{})
	}
	for k, n := range m.Nodes {
		if k > 0 {
			rows = append(rows, transcriptRow{})
		}
		ref := nodeRefAt(m, k)
		if coords {
			rows = append(rows, t.coordRow(ref, nodeCoordAt(n)))
		}
		for _, l := range t.renderNode(n, ref) {
			// Rows are stored already clipped and gutter-prefixed (their
			// unselected resting form) so a frame that touches nothing
			// allocates nothing; see plainNodeRow. collapseSGR then strips the
			// rendition churn glamour emits per cell — 3/4 of the retained row
			// text, and of the bytes each painted frame puts on the wire. It is
			// applied here, on the way into the cache, so the saving is paid
			// once and collected on every frame; see sgr.go.
			//
			// FUTURE WORK, evaluated and deferred at the B+E merge: E proposed
			// doing the collapse inside the memoized render.Prose instead, so
			// the cost is paid once per (markdown, width) rather than once per
			// row-cache fill, and the live inline path benefits too. Measured on
			// this stack the prize is real — with the Prose memo warm, the
			// collapse is 1.2 ms and ~316 KB of the 3.1 ms it takes to fill the
			// cache for a heavy aria (BenchmarkTranscriptHeavyEnter: 1.86 ms
			// without it, 3.08 ms with), which is what a width change, a Ctrl-O
			// and every landing page pay. Sampling says the transform commutes
			// with clipToWidth, including on truncating clips, so the row cache
			// would still come out collapsed.
			//
			// It is deferred because the move is not local. collapseSGR would
			// have to live in internal/render (cli imports render, not the other
			// way), while its proof apparatus — sgr_vt_test.go's VT model — is
			// needed by tests on BOTH sides (the golden cell-level proof and the
			// painter composition tests are cli's), so the model wants a third
			// package (internal/render/sgrtest) before the transform can move at
			// all. And the same change silently rewrites the bytes the live
			// inline renderer (ldrender.Incipit) puts on the terminal, a path
			// this campaign has no cell-level coverage for. The follow-up is
			// therefore: extract the model, then move the transform, then add a
			// commutation fuzz target for clipToWidth and incipit coverage —
			// four changes, none of which belong in a merge commit.
			rows = append(rows, transcriptRow{text: collapseSGR(plainNodeRow(l, t.w)), ref: ref})
		}
	}
	// Committed rows are retained for as long as the page is, so hand back an
	// exactly-sized array: append growth leaves up to 65% slack, and at 32
	// bytes a row that is real memory held for the whole session.
	if cap(rows) > len(rows) {
		rows = append(make([]transcriptRow, 0, len(rows)), rows...)
	}
	return cachedMessage{rows: rows}
}

func (t *transcript) renderNode(n livedoc.Node, ref nodeRef) []string {
	width := t.w - 2
	if width < 1 {
		width = 1
	}
	if view, ok := t.view.(expandableNodeView); ok {
		return view.RenderExpanded(n, width, t.tick, t.expanded[ref])
	}
	return t.view.Render(n, width, t.tick)
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
//	─────────…──────────────── aria <id> · 48–97/97 live ───
//	<mantra> · thinking ⠧ · ctx … · cost … · <time> · ? help · ! status
//
// The rule row carries the identity + scroll position right-aligned; the
// status row is plain left-aligned text (fig status at a glance). In search,
// the status row becomes the query prompt.
func (t *transcript) footerRows(total, body int) (rule, status string) {
	pos := ""
	if total > body {
		end := t.offset + body
		if end > total {
			end = total
		}
		pos = fmt.Sprintf("%d–%d/%d", t.offset+1, end, total)
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
// yet started) user prompts, oldest first. The list is a snapshot the input
// loop refreshes asynchronously via figaro.queued (setting queuedList /
// queuedErr under the shared render mutex). Purely observational — there is
// no cancellation surface here.
func (t *transcript) queuedPanelLines() []string {
	rows := []string{"", "  queued prompts"}
	switch {
	case t.queuedErr != "":
		rows = append(rows, "  "+t.queuedErr)
	case len(t.queuedList) == 0:
		rows = append(rows, "  (none)")
	default:
		for i, p := range t.queuedList {
			head := firstLineTrim(p)
			rows = append(rows, fmt.Sprintf("  %2d. %s", i+1, head))
		}
	}
	if max := t.h - 4; len(rows) > max && max > 0 {
		rows = rows[:max]
	}
	for i, r := range rows {
		rows[i] = "\x1b[2m" + clipToWidth(r, t.w) + "\x1b[0m"
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
	t.showQueued = true
	if t.queuedFetch != nil {
		t.queuedFetch()
	}
}

// setQueued updates the panel's cached snapshot. Called by the input loop
// under the shared render mutex from the fetch goroutine's completion.
func (t *transcript) setQueued(prompts []string, errMsg string) {
	t.queuedList = prompts
	t.queuedErr = errMsg
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
	rows := make([]string, 0, len(body)+3)
	rows = append(rows, "")
	rows = append(rows, body...)
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
	t.prefix = ""
	buf = append(buf, "\x1b[?2026h"...)
	base := t.prev
	if plan, ok := t.planScroll(screen); ok {
		buf = appendScroll(buf, plan)
		base = t.predBuf
	}
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(base) {
			old = base[r]
		}
		if screen[r] == old {
			continue
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
	t.checkOlder = true
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
func pagerToggleTools(t *transcript)  { t.toggleSelectedTools() }

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
	t.search = &transcriptSearch{
		query: q, pages: append([]transcriptPage(nil), t.pages...),
		newer: append([]pageDesc(nil), t.newer...), offset: t.offset,
		follow: t.follow, noMoreOlder: t.noMoreOlder,
		direction: pageOlder,
	}
	t.stopFollowing()
	if t.hasNewerHistory() {
		t.search.direction = pageNewer
		t.checkNewer = true
	} else {
		t.checkOlder = true
	}
}

// findRepeat jumps to the next (delta > 0) or previous (delta < 0) match of
// the persistent matchQuery. Wraps within loaded lines. If nothing matches
// in-window, falls back to the paged-search worker in the current direction.
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
	// Nothing in the loaded window; page in the correct direction.
	t.search = &transcriptSearch{
		query: q, pages: append([]transcriptPage(nil), t.pages...),
		newer: append([]pageDesc(nil), t.newer...), offset: t.offset,
		follow: t.follow, noMoreOlder: t.noMoreOlder,
		direction: pageOlder,
	}
	t.stopFollowing()
	if delta > 0 && t.hasNewerHistory() {
		t.search.direction = pageNewer
		t.checkNewer = true
	} else {
		t.checkOlder = true
	}
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
			// searchText strips the gutter column plainNodeRow baked in.
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

func (t *transcript) finishSearch(found bool) {
	if found || t.search == nil {
		return
	}
	origin := t.search
	t.pages = origin.pages
	t.invalidateWindow()
	t.newer = origin.newer
	t.offset = origin.offset
	t.follow = origin.follow
	t.noMoreOlder = origin.noMoreOlder
	t.search = nil
	t.checkOlder, t.checkNewer = false, false
	t.pruneCaches()
}

func (t *transcript) wrapSearchOlder() {
	if t.search == nil {
		return
	}
	origin := t.search
	t.pages = append([]transcriptPage(nil), origin.pages...)
	t.invalidateWindow()
	t.newer = append([]pageDesc(nil), origin.newer...)
	t.offset = origin.offset
	t.follow = false
	t.noMoreOlder = origin.noMoreOlder
	t.checkNewer = false
	if t.noMoreOlder {
		t.finishSearch(false)
		return
	}
	t.checkOlder = true
	origin.direction = pageOlder
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
