package cli

import (
	"fmt"
	"html"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/mark"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
)

// transcript is the alt-screen pager over a paged conversation: a fixed canvas
// with a bounded message window that follows the live tail at the bottom.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client
	status *sessionStatus

	active bool
	// pit is the one transient region below the transcript; none of its
	// contents may touch the status bar.
	pit         pit
	queuedByKey bool // opened by 'Q', not because the queue filled

	// full is the pager's fullscreen disposition, inherited by whatever pit is
	// open; it applies only while the pit has the focus.
	full bool
	// A fullscreen pit recedes when the transcript takes focus (`T`) rather
	// than closing.
	focused pitFocus
	// queueDismissed suppresses the auto-open after a deliberate close;
	// without it the next poll reopens the pit.
	queueDismissed bool
	queuedRows     []string // pre-rendered, for the inline trailer
	queued         []queuedItem
	queueOpen      map[uint64]bool
	// "the queue is empty" is only ever said about a queue this client
	// actually read; queuedErr keeps a failure from reporting as emptiness.
	queuedErr   string
	queuedFetch func() // async refresh; set by the input loop
	// command is called under the render lock, like every hook here: an
	// implementation must hand off (one `go` statement), since taking the lock
	// deadlocks the input goroutine and blocking on an RPC stops the keyboard.
	// nil in a fixture.
	command func(string)
	// completer: set by the input loop, which owns the router.
	completer func(string) []string
	// composerCompleter is Tab in the '>' draft; defaulted, since its sources
	// are local.
	composerCompleter func(string) []string
	// sendComposer is called holding the render lock: it must hand off.
	sendComposer   func(string)
	decideComposer func(question string, choices []string)
	decision       *decision
	// openForm must not take the render lock: every hook here is called from
	// dispatch, which holds it.
	openForm func()
	// dropRow: next names the row the cursor falls onto, where a kill should
	// leave the pager.
	dropRow func(pit, id, next string)
	// attendAria and ariaHop dial, so they hand off to a goroutine on the same
	// rule as openForm.
	attendAria   func(id string)
	ariaHop      func(dir int)
	attendParent func()
	confirm      *confirmAsk
	// openPath takes the terminal, so it hands off like every other hook.
	openPath func(path string)
	w, h     int
	tick     int

	prev   []string // the frame the terminal is holding
	prefix string   // one-shot escapes emitted with the next frame
	// altPending/altOn pair the alt-screen switch: enter() queues it, the
	// paint that emits it sets altOn, leave() writes the exit only when altOn,
	// so a pager that never painted never writes 2J + ?1049l to the real
	// screen.
	altPending bool
	altOn      bool
	// The painter's model of the screen (prev) is only true while figaro is
	// the only writer; lastFull is when it was last repainted unconditionally,
	// and now is injectable for tests.
	now      func() time.Time
	lastFull time.Time
	lineKey  []sliceKey // slice owning each line, for resize anchoring
	offset   int        // top line of the viewport
	// wantTop is a standing request for the beginning, armed by Home/gg.
	wantTop bool
	kept    bool
	follow  bool
	pending string

	comments     *commentIndex
	commentsOpen map[nodeRef]bool

	// render() defers while a batch is open or the frame-rate gate declines;
	// flush() draws the deferred frame.
	batch   int
	dirty   bool
	gate    func() bool // "may I paint now?": a false answer owes a later flush()
	painted func()

	inSearch bool
	// searchDir: 1 for '/', -1 for '?'; n repeats, N reverses.
	searchDir  int
	query      string
	matchQuery string // persistent query: highlights + n/N target

	// box is which text box owns the keyboard; both are one keyMode (modeBox)
	// and one pit.
	box boxKind
	// cmdline is the ':' box's editor.
	cmdline lineEditor
	// draft outlives the drawer: Esc puts it away and '>' brings it back.
	draft lineEditor
	// composerMode is which keymap the drawer answers to (composer_normal.go).
	composerMode composerKeys
	// composerMark/composerKind are the drawer's highlight, a rune index and
	// its kind.
	composerMark int
	composerKind visualKind
	// composerHeld: a ':' line was opened over the drawer, so closing it
	// returns to the draft.
	composerHeld bool
	optline      lineEditor
	askOptions   bool
	menu         *completionMenu
	// notes lives here, not on the status, because a status is replaced on
	// every hop.
	notes *notificationStore
	// noticeTTL outlives the status for the same reason notes does.
	noticeTTL *time.Duration
	jumpNote  string
	jump      *transcriptJump

	// Older history is pulled only when the viewport nears the window floor.
	search *transcriptSearch

	// The window is the store's own tail, the interval [from, inf): there is
	// no second copy.
	from aria.Anchor

	tailTuned bool
	tailWant  int
	// windowRev: every mutation of the window's floor goes through
	// invalidateWindow, so the line index refills lineKey rather than
	// inferring the move.
	windowRev uint64
	// rowRev: every path that drops cached rows bumps it, or the index stamp
	// would hold over rows that are gone.
	rowRev uint64

	// rowCache rows carry no selection cue and no search highlight, so it is a
	// pure function of (message, width); the cue and highlight are applied per
	// painted row.
	rowCache map[sliceKey]cachedMessage
	// stickyCache is keyed and invalidated like rowCache, whose rows these are.
	stickyCache map[sliceKey]stickyQuestion
	// openMemo memoizes each block of the open message, so a tick redraws only
	// what the stream touched.
	openMemo  openMemo
	cacheW    int
	selection nodeSelection
	visual    visualSelection
	expanded  map[nodeRef]bool
	// adorned is the form-delta fold state; a tool has both, Enter opens the
	// pair, d and t one each.
	adorned map[nodeRef]bool

	// index maps line space to message rows so scrolling never materializes
	// rows it will not paint.
	index   lineIndex
	ruleStr string
	ruleW   int
	// Reused buffers, each with one owner so none can alias.
	rowBuf      []string  // valid until the next render()
	frameRefs   []nodeRef // the node behind each body row of the last painted frame
	lineBuf     []string  // valid until the next lines()
	keepBuf     map[int]bool
	paintBuf    []byte
	predBuf     []string
	keysNew     []uint32
	keysOld     []uint32
	screenSpare []string
}

// Only the viewport is restored when a walk finds nothing; history it paged in
// stays in the store.
type transcriptSearch struct {
	query  string
	offset int
	follow bool
}

const (
	seekForward  = 1
	seekBackward = -1
)

type transcriptPageRequest struct {
	at    aria.Anchor
	limit int
	seek  int
	jump  *transcriptJump
	fill  *aria.Gap
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	t := &transcript{
		out: out, view: view, client: client, w: w, h: h,
		rowCache: map[sliceKey]cachedMessage{}, stickyCache: map[sliceKey]stickyQuestion{},
		expanded: map[nodeRef]bool{}, adorned: map[nodeRef]bool{},
		notes: newNotificationStore(),
	}
	t.setStatus(newSessionStatus(figaroID, startedAt))
	return t
}

// Every status is bound to the same notification store, or the unread mark would
// reset on every hop.
func (t *transcript) setStatus(s *sessionStatus) {
	if s == nil {
		return
	}
	s.notes = t.notes
	if t.noticeTTL != nil {
		s.setNoticeTTL(*t.noticeTTL)
	}
	t.status = s
}

func (t *transcript) setNoticeTTL(d time.Duration) {
	t.noticeTTL = &d
	if t.status != nil {
		t.status.setNoticeTTL(d)
	}
}

// Autowrap must be off: a wide glyph tipping the bottom-right cell past the last
// column with autowrap on scrolls the whole screen up a row.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pending, t.inSearch, t.query, t.matchQuery = "", false, "", ""
	t.searchDir = 1
	t.box, t.jumpNote, t.jump = boxNone, "", nil
	t.cmdline.reset()
	t.menu = nil
	// The pager owns retention while it is up: the client's count-based trim
	// drops the oldest, which would evict a reader scrolled up; evictStale
	// bounds the store against the window instead.
	t.client.SetClosedLimit(0)
	t.from = aria.Anchor{}
	t.invalidateWindow() // a fresh session rebuilds the window from the tail
	t.resetToTail()
	// Queued, not written: a pager that never paints never switches the
	// terminal (altPending/altOn).
	t.prefix = altScreenOn + ldmouse.Enable
	t.altPending = true
	t.render()
}

// Mouse reporting is disabled before the alt-screen swap so no stray \x1b[<...M
// leaks into the shell.
func (t *transcript) leave() {
	t.active = false
	t.selection = nodeSelection{}
	t.prefix = ""
	t.altPending = false
	t.client.SetClosedLimit(transcriptTailLimit)
	if t.altOn {
		io.WriteString(t.out, ldmouse.Disable+altScreenOff)
		t.altOn = false
	}
	t.prev = nil
}

// stopFollowing first: it pins the offset at the live position, so the motion is
// relative to what is on screen.
func (t *transcript) scroll(delta int) {
	t.wantTop = false
	t.stopFollowing()
	t.offset += delta
	if _, maxOff := t.layout(len(t.footLines())); t.offset > maxOff {
		pagerTail(t)
	}
}

// Upward out of live, detaching is the motion: the live padding row becomes
// content and nothing scrolls. Deliberate jumps (u/PgUp, gg) do not route through
// here.
func (t *transcript) scrollNotch(delta int) {
	if t.follow && delta < 0 {
		t.stopFollowing()
		return
	}
	t.scroll(delta)
}

// render clamps the offset, even when the frame itself is deferred.
func (t *transcript) scrollBy(delta int) {
	if !t.active {
		return
	}
	t.scrollNotch(delta)
	t.render()
}

const (
	transcriptPageSize    = 30
	transcriptMinPageSize = 6
	transcriptPageLimit   = 3
	transcriptTailLimit   = 2 * transcriptPageSize
)

// A var so the geometry sweep in transcript_geometry_bench_test.go can measure the
// tradeoff.
var transcriptWindowRows = 2400

// heldWindow reads the line index, which already computes both counts per frame.
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

func pageRowBudget() int { return transcriptWindowRows / transcriptPageLimit }

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

// Cold, it starts at the floor rather than the ceiling: tuneTail grows the window
// into the budget, invisibly, since the added rows are above a viewport pinned to
// the tail.
func (t *transcript) tailKeep() int {
	if t.tailWant > 0 {
		return t.tailWant
	}
	return transcriptMinPageSize
}

// Two screens of runway: at wheel speed a few hundred milliseconds, enough for a
// local daemon round trip.
const transcriptPrefetchScreens = 2

func (t *transcript) wantOlder() bool {
	if t.search != nil {
		return true
	}
	return t.offset < transcriptPrefetchScreens*t.h
}

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

// Never raises the floor: that is resetToTail's job, and only while following.
func (t *transcript) lowerFloor(a aria.Anchor) {
	if !a.Less(t.from) {
		return
	}
	t.from = a
	t.invalidateWindow()
}

// At the floor a search that found nothing goes home and a jump resolves against
// the beginning.
func (t *transcript) reachedFloor() {
	t.wantTop = false
	t.finishSearch(false)
	t.jumpAdvance()
	t.render()
}

func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	if !t.active {
		return transcriptPageRequest{}, false
	}
	if t.jump != nil {
		if at, dir, ok := t.jumpSeek(); ok {
			return transcriptPageRequest{at: at, seek: dir, jump: t.jump}, true
		}
		return transcriptPageRequest{}, false
	}
	for t.wantOlder() {
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
	if gap := t.gapNear(); gap != nil {
		hole := *gap
		return transcriptPageRequest{fill: &hole}, true
	}
	if !t.wantOlder() {
		return transcriptPageRequest{}, false
	}
	if t.atAriaFloor() {
		t.reachedFloor()
		return transcriptPageRequest{}, false
	}
	return transcriptPageRequest{at: t.from, limit: t.pageMessages()}, true
}

func (t *transcript) absorbOlder(gained []aria.Message, anchor sliceKey, within int) {
	if t.search != nil {
		if t.findPage(t.search.query, gained) {
			t.search = nil
		}
		return
	}
	t.buildIndex()
	if t.wantTop {
		// Hold at the top of what is now held; the floor clears wantTop.
		t.offset = 0
		if t.atAriaFloor() {
			t.wantTop = false
		}
	} else {
		t.restoreViewportAnchor(anchor, within)
	}
	t.jumpAdvance()
}

func (t *transcript) applyPage(req transcriptPageRequest, page aria.Page) {
	if !t.active {
		return
	}
	anchor, within := sliceKey(0), 0
	if t.search == nil {
		anchor, within = t.viewportAnchor()
	}
	if req.seek != 0 {
		t.applySeek(req, page)
		return
	}
	t.client.SetMoreBefore(page.More.Before)
	msgs := t.client.Apply(page, aria.Quiet)
	if len(msgs) == 0 {
		// Nothing on the wire reports where an aria begins: a read that
		// hands back nothing new is the floor.
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	// A page that does not lower the floor ends the walk, whatever the wire
	// says about more.
	before := t.from
	t.lowerFloor(anchorOf(msgs[0]))
	if t.from == before {
		t.client.SetMoreBefore(false)
		t.reachedFloor()
		return
	}
	t.absorbOlder(msgs, anchor, within)
	if t.search != nil {
		return // still walking
	}
	t.render()
}

func (t *transcript) applySeek(req transcriptPageRequest, page aria.Page) {
	t.client.Apply(page, aria.Quiet)
	t.invalidateWindow()
	if t.jump == nil || req.jump != t.jump {
		return
	}
	if len(page.Parts) == 0 {
		if t.jump.target.start {
			t.client.SetMoreBefore(false)
			t.settle()
			t.jumpAdvance()
			if t.jump == nil {
				t.render()
				return
			}
		}
		t.abandonJump(t.jump.target.missing())
		t.render()
		return
	}
	from, _ := page.Span()
	if !page.More.Before && !t.from.Less(from) {
		t.client.SetMoreBefore(false)
	}
	t.lowerFloor(from)
	j := t.jump
	if j.target.travel != 0 || j.target.start {
		part := page.Parts[0]
		if j.target.travel < 0 {
			part = page.Parts[len(page.Parts)-1]
		}
		if j.target.travel > 0 && int(part.ID) <= j.target.turn ||
			j.target.travel < 0 && int(part.ID) >= j.target.turn {
			t.abandonJump(j.target.missing())
			t.render()
			return
		}
		j.target = jumpTarget{turn: int(part.ID)}
		j.asked = nil
	}
	t.settle()
	t.jumpAdvance()
	if t.jump != nil && t.jump.asked != nil {
		t.abandonJump(t.jump.target.missing())
	}
	t.render()
}

func anchorOf(m aria.Message) aria.Anchor {
	return aria.Anchor{Turn: uint64(m.Turn), Node: m.From}
}

func pageMessages(p aria.Page) []aria.Message {
	return aria.NewClient().Apply(p, aria.Quiet)
}

// sliceKey packs turn (high bits) and node offset (low 20) into one integer: a
// two-word key measurably slowed the per-frame row cache. 2^20 nodes in a turn is
// not reachable.
type sliceKey int64

const sliceKeyFromBits = 20

func keyOf(m aria.Message) sliceKey { return sliceKeyOf(m.Turn, m.From) }

func sliceKeyOf(turn int, from uint64) sliceKey {
	return sliceKey(int64(turn)<<sliceKeyFromBits | int64(from))
}

func (k sliceKey) turn() int { return int(k >> sliceKeyFromBits) }

func (k sliceKey) from() uint64 { return uint64(k) & (1<<sliceKeyFromBits - 1) }

// Recomputes one anchor; Store.TailFrom walks backward, so this costs the window's
// size, not the aria's.
func (t *transcript) resetToTail() {
	t.wantTop = false
	keep := t.tailKeep()
	n := t.client.Count()
	if keep > n {
		keep = n
	}
	from, held := t.client.TailFrom(keep)
	if !held {
		// Nothing closed is retained (a cold join to a long turn): the
		// zero anchor would read as the tail, so anchor on the open
		// message.
		if open := t.openMessage(); open != nil {
			from = anchorOf(*open)
		}
	}
	if t.from == from {
		return
	}
	t.from = from
	t.invalidateWindow()
	t.tailTuned = false
	t.evictStale()
	t.pruneCaches()
}

// transcriptRetainRows bounds what the store holds behind the window: the return
// trip's worth, four windows.
var transcriptRetainRows = 4 * transcriptWindowRows

// A message is anywhere from 4 rows to 400, so a message count is not a memory bound.
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

// evictStale never evicts what the window shows.
func (t *transcript) evictStale() {
	floor, ok := t.client.TailFrom(t.retainMessages())
	if !ok {
		return
	}
	if t.from.Less(floor) {
		floor = t.from
	}
	t.client.EvictBefore(floor)
}

// tuneTail grows a window that does not fill its budget (or the viewport) and
// shrinks one that overshot; reports whether the window changed.
func (t *transcript) tuneTail() bool {
	if t.tailTuned || !t.follow {
		return false
	}
	_, have := t.heldWindow()
	if have == 0 {
		return false
	}
	budget := pageRowBudget()
	fits, rows := t.tailFit(budget)

	// Overshot: the cut is a fact about measured heights, not a prediction
	// from an average, so re-deriving it converges.
	if fits < have {
		t.tailWant = fits
		t.tailTuned = true // the cut is the fixed point
		before := t.from
		t.resetToTail()
		t.tailTuned = true // resetToTail cleared it; the cut still stands
		return t.from != before
	}

	// Under budget: the average is a hint for how far to jump, and nowhere
	// else the control law.
	full := rows >= budget || have >= transcriptPageSize
	if full && t.index.total >= t.h {
		t.tailWant = have
		t.tailTuned = true
		return false
	}
	want := have + max(t.pageMessages(), 1)
	before := t.from
	t.tailWant = want
	t.resetToTail()
	if t.from == before {
		t.tailTuned = true // the whole aria is held
		return false
	}
	return true
}

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

// The one authority on "the retained window changed".
func (t *transcript) invalidateWindow() { t.windowRev++ }

func (t *transcript) pruneCaches() {
	// Once per frame while following: the turn set is reused.
	keep := t.keepBuf
	if keep == nil {
		keep = make(map[int]bool, transcriptPageSize*transcriptPageLimit)
		t.keepBuf = keep
	} else {
		clear(keep)
	}
	// Rows follow the store, not the window: scrolling back over a message the
	// store still holds must cost neither I/O nor a re-render.
	t.client.ForEachIn(aria.Anchor{}, windowEnd, func(m aria.Message) bool {
		keep[m.Turn] = true
		return true
	})
	// The open turn is the live suffix, not in the store's window; pruning it
	// loses `expanded`, which is user state, on every following frame.
	if open := t.openMessage(); open != nil {
		keep[open.Turn] = true
	}
	for k := range t.rowCache {
		if !keep[k.turn()] {
			delete(t.rowCache, k)
		}
	}
	for _, state := range []map[nodeRef]bool{t.expanded, t.adorned} {
		for ref := range state {
			if !keep[ref.turn] {
				delete(state, ref)
			}
		}
	}
	// Counting what was deleted would cost as much as the bump.
	t.rowRev++
}

// On the frame path: no slice of the window is materialized.
func (t *transcript) forEachMessage(fn func(aria.Message)) {
	t.client.ForEachIn(t.from, windowEnd, func(m aria.Message) bool {
		fn(m)
		return true
	})
}

var windowEnd = aria.Anchor{Turn: ^uint64(0), Node: ^uint64(0)}

func (t *transcript) subjectID() string {
	if t.status == nil {
		return ""
	}
	return t.status.figaroID
}

func (t *transcript) retained() (msgs, rows int) {
	if t.client != nil {
		msgs = t.client.Count()
	}
	for _, c := range t.rowCache {
		rows += len(c.rows)
	}
	return msgs, rows
}

func (t *transcript) messages() []aria.Message {
	out := make([]aria.Message, 0, t.client.Count())
	t.forEachMessage(func(m aria.Message) { out = append(out, m) })
	return out
}

// Non-zero means the window holds only the tail of its oldest turn, so the
// backward fetch must anchor on the node.
func (t *transcript) oldestFrom() uint64 { return t.from.Node }

func (t *transcript) setSize(w, h int) {
	if w == t.w && h == t.h {
		return
	}
	t.w, t.h = w, h
	t.prev = nil
	t.invalidateRows()
}

func (t *transcript) resize(w, h int) {
	// Anchor on the slice at the viewport top: a width change re-wraps rows.
	anchor, within := t.viewportAnchor()
	// Visual points are pinned to source runes before the width changes
	// (visualPin).
	var pinCur, pinAnchor visualPin
	if t.visual.active() {
		t.buildIndex()
		pinCur = t.visualPin(t.visual.cursor)
		if t.visual.highlighted() {
			pinAnchor = t.visualPin(t.visual.anchor)
		}
	}
	t.w, t.h = w, h
	// The row cache is not keyed by width: without this a width change
	// re-serves rows composed for the old width.
	t.invalidateRows()
	// Nil: paint repaints every row, the blank ones included, or the
	// terminal's post-resize leftovers stay in the gaps.
	t.prev = nil
	t.buildIndex()
	if t.visual.active() {
		t.visual.cursor = t.visualUnpin(pinCur)
		if t.visual.highlighted() {
			t.visual.anchor = t.visualUnpin(pinAnchor)
		}
		t.visualClampCol()
	}
	t.restoreViewportAnchor(anchor, within)
	if t.showing(pitQueue) {
		t.refreshQueuePit()
	}
	t.render()
}

// Slice, not turn: a page landing can prepend an earlier slice of the turn at the
// top, and a turn-granular anchor would restore to its head.
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
	t.rowRev++
	t.rowCache = map[sliceKey]cachedMessage{}
	// Whatever voided the committed rows voided the open message's blocks too.
	t.openMemo = openMemo{}
}

// screenMoved: something else wrote to the screen, so the next frame repaints in full.
func (t *transcript) screenMoved() { t.prev = nil }

func (t *transcript) resyncDue() bool {
	if transcriptResyncInterval <= 0 {
		return false
	}
	if t.now == nil {
		t.now = time.Now
	}
	return t.now().Sub(t.lastFull) >= transcriptResyncInterval
}

// Unchanged rows are usually the same string values, so most comparisons are a
// pointer and a length.
func screensEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (t *transcript) lines() []string {
	t.buildIndex()
	t.lineBuf = t.window(0, t.index.total, t.lineBuf)
	return t.lineBuf
}

// Memoized: strings.Repeat per message per frame is 16 KB/frame of identical strings.
func (t *transcript) transRule() string {
	if t.ruleStr == "" || t.ruleW != t.w {
		t.ruleStr, t.ruleW = dimTransRule(t.w), t.w
	}
	return t.ruleStr
}

func (t *transcript) openMessage() *aria.Message { return t.client.Open() }

// stopFollowing pins the viewport where it was; settle first, since both are only
// true while following.
func (t *transcript) stopFollowing() {
	if !t.follow {
		return
	}
	t.settle()
	t.follow = false
	_, t.offset = t.layout(len(t.footLines()))
}

// settle converges the tail window on the row budget; stopFollowing must not
// freeze an unconverged window.
func (t *transcript) settle() {
	t.buildIndex()
	for range 3 {
		if !t.tuneTail() {
			break
		}
		t.buildIndex()
	}
}

func (t *transcript) footLines() []string {
	rows := t.inputDrawerLines()
	if rows == nil {
		if !t.pit.open() {
			return nil
		}
		t.pit.full = t.fullPit()
		rows = t.pit.lines(t.w, t.pitRoom())
	}
	// Fullscreen is the pit and nothing else: no rule, no page position.
	if t.fullPit() {
		return rows
	}
	return append([]string{t.transcriptRule()}, rows...)
}

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

type pitFocus uint8

const (
	focusPit        pitFocus = iota // a pit you opened is a pit you are in
	focusTranscript                 // `T`: read the conversation, keep the pit
)

// A fullscreen pit recedes when the transcript takes the keys.
func (t *transcript) fullPit() bool {
	return t.full && (t.pit.open() || t.box == boxComposer) && t.focused == focusPit
}

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

// The only place this arithmetic is done: the pane, minus the bar (one row, or
// three when it wraps), the blank above it, the rule, and a row of conversation.
func (t *transcript) pitRoom() int {
	if t.fullPit() {
		// Fullscreen: everything but the status bar and the blank above
		// it; no rule is drawn.
		return max(t.h-t.barRows()-1, 1)
	}
	return max(t.h-t.barRows()-5, 1)
}

// Without re-entering footLines, which calls transcriptRule, which would call this.
func (t *transcript) layoutNow() (body, maxOff int) {
	foot := 0
	if rows := t.inputDrawerLines(); rows != nil {
		foot = len(rows)
	} else if t.pit.open() {
		t.pit.full = t.fullPit()
		foot = len(t.pit.lines(t.w, t.pitRoom()))
	}
	return t.layout(foot + 1)
}

func padTo(s string, n int) string {
	if w := runewidth.StringWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// A row is selectable because its verb said what it names, not because its text
// looks aria-shaped.
func (t *transcript) setCmdOut(title string, rows []pitRow) {
	t.queuedByKey = false
	t.pit.showList(pitOutput, ":"+title, rows)
	t.pit.focus(t.status.figaroID)
	t.focused = focusPit
	t.render()
}

// The bar is not always one row: on a narrow pane it is three, and reserving one
// while painting three pushes the conversation off the top.
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

func (t *transcript) renderMsgBase(m aria.Message) cachedMessage {
	return t.renderMsg(m, nil)
}

// Markdown layout is the most expensive thing the pager does and the open message
// is redrawn every frame: the memo makes a frame cost what the stream changed.
func (t *transcript) renderOpenMsg(m aria.Message) cachedMessage {
	if t.openMemo.key != keyOf(m) || t.openMemo.w != t.w || t.openMemo.blocks == nil {
		t.openMemo = openMemo{key: keyOf(m), w: t.w, blocks: map[int]*memoBlock{}}
	}
	for _, b := range t.openMemo.blocks {
		b.reused = false
	}
	return t.renderMsg(m, &t.openMemo)
}

func (t *transcript) renderMsg(m aria.Message, memo *openMemo) cachedMessage {
	c := t.rowComposer(m)
	if memo != nil {
		c.Memo = memo.compose
	}
	composed := c.Message(m, t.w)
	rows := make([]transcriptRow, 0, len(composed))
	for i := 0; i < len(composed); {
		r := composed[i]
		if r.Block == ldrender.BlockChrome {
			rows = append(rows, transcriptRow{text: r.Text})
			i++
			continue
		}
		if memo != nil {
			if kept, ok := memo.kept(r.Block); ok {
				rows = append(rows, kept...)
				for i < len(composed) && composed[i].Block == r.Block {
					i++
				}
				rows = t.appendNoteRows(rows, m, r.Block)
				continue
			}
		}
		block := r.Block
		start := len(rows)
		for ; i < len(composed) && composed[i].Block == block; i++ {
			rows = append(rows, t.pagerRow(m, composed[i]))
		}
		if memo != nil {
			memo.keep(block, rows[start:len(rows):len(rows)])
		}
		rows = t.appendNoteRows(rows, m, block)
	}
	return cachedMessage{rows: rows}
}

func (t *transcript) pagerRow(m aria.Message, r ldrender.Row) transcriptRow {
	ref := blockRef(m, r.Block)
	if r.Delta > 0 {
		ref = deltaRefOf(ref, r.Delta)
	}
	// Stored clipped (plainNodeRow) so an untouched frame allocates nothing;
	// sgrCollapse strips the per-cell rendition churn glamour emits, 3/4 of
	// the row bytes.
	return transcriptRow{
		text:   sgrCollapse(plainNodeRow(r.Text, t.w)),
		ref:    ref,
		mark:   r.Mark,
		gutter: r.Gutter,
		spine:  r.Spine,
		chrome: r.Chrome,
	}
}

func (t *transcript) rowComposer(m aria.Message) ldrender.Composer {
	c := ldrender.Composer{
		// The pager view may open arguments as well as output
		// (ariaView.gesture).
		View: pagerView(t.view), Header: messageHeader, Rule: t.transRule, InputHeader: inputHeader, Tick: t.tick,
		Expanded: func(block int) bool { return t.expanded[blockRef(m, block)] },
		Adorn: func(block int, n livedoc.Node, deltas map[string]livedoc.FormDelta, w int) ldrender.Adornment {
			ref := blockRef(m, block)
			return buildAdornment(adornerFor(block, n), deltas, w, t.adorned[ref])
		},
		Quote: quoteRows,
	}
	// The address is composed always and drawn only under M-m: the toggle is a
	// paint-time decision and the row cache does not know it.
	layout := t.coordFormat()
	c.Mark = func(block int, n livedoc.Node) string {
		ref := blockRef(m, block)
		return term.Dim(coordLabel(ref.turn, ref.index, nodeCoordAt(n), layout))
	}
	return c
}

func (t *transcript) render() {
	if !t.active {
		return
	}
	// Clamp the low side before the gate: the offset is read off the frame
	// path too (viewportAnchor, the search wrap-around), and a negative index
	// panics.
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

// beginBatch/endBatch bracket state changes that must produce one frame.
func (t *transcript) beginBatch() { t.batch++ }

func (t *transcript) endBatch() {
	if t.batch > 0 {
		t.batch--
	}
	if t.batch == 0 && t.dirty {
		t.dirty = false
		t.render() // re-checks the gate, which may defer once more
	}
}

// Whoever refuses a frame owes a later flush, so the final state is always on screen.
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

// The ceiling is for a stream, not for an answer: a subject switch is one discrete
// thing and appears at once. A batch still holds it.
func (t *transcript) renderNow() {
	if !t.active || t.batch > 0 {
		t.render()
		return
	}
	if t.offset < 0 {
		t.offset = 0
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

func (t *transcript) renderFrame() {
	// A pane under four rows has nowhere to draw the footer and would index
	// screen[-2]: skip the frame.
	if t.h < 4 {
		return
	}
	// settle reads the index, never a full materialization of the window.
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
	// Only the rows about to be painted are materialized, decorated and
	// highlighted.
	t.rowBuf = t.window(t.offset, t.offset+body, t.rowBuf)
	// The row->node map is recorded from the geometry actually painted:
	// re-deriving it at click time would consult an offset a live token may
	// have moved.
	t.frameRefs = t.rowRefs(t.offset, t.offset+body, t.frameRefs[:0])
	copy(screen[:body], t.rowBuf)
	// The header covers rows: they address nothing while they cannot be seen.
	head := t.stickyLines(t.activeHighlight(), t.selectionSpan())
	copy(screen[:len(head)], head)
	for i := range min(len(head), len(t.frameRefs)) {
		t.frameRefs[i] = nodeRef{}
	}
	// Bottom-aligned: the live padding row belongs above the transcript rule,
	// not between the pit and its rule.
	if t.fullPit() {
		// Fullscreen obscures the conversation rather than dimming it.
		for r := range screen {
			if r < t.h-2 {
				screen[r] = ""
			}
		}
	}
	// The row above the rule is blank while following (layout reserved it).
	rule, bar := t.footerRows(total, body)
	// A stanza taller than the pane keeps its tail: the rows nearest the bar
	// are the ones being read.
	start := t.h - 1 - len(bar) - len(foot)
	clipped := false
	if start < 0 {
		foot = foot[-start:]
		start = 0
		clipped = true
	}
	// The closing rule carries the gutter junction (joinRule): the pit's own
	// when a panel is up, the footer's otherwise; a clipped stanza has lost
	// its rule, a fullscreen pit has no conversation under it.
	floor := t.lineCut(t.offset+body-1, t.offset+body)
	if len(foot) > 0 && !clipped && !t.fullPit() && start > 0 {
		foot[0] = joinRule(foot[0], floor, gutterCut{})
	}
	for k, l := range foot {
		if r := start + k; r >= 0 && r < t.h-len(bar) {
			screen[r] = l
		}
	}
	if t.pit.open() || t.inSearch || t.boxOpen() {
		// No closing rule and no key hints under an open pit: one rule
		// above, the rows, a blank, the bar.
		rule = ""
	}
	if top := t.h - 1 - len(bar); top >= 0 {
		if rule != "" && len(foot) == 0 && top > 0 {
			rule = joinRule(rule, floor, gutterCut{})
		}
		screen[top] = rule
		for i, row := range bar {
			if r := top + 1 + i; r >= 0 && r < t.h {
				screen[r] = row
			}
		}
	}
	t.paint(screen)
}

// barRows is asked before the bar is drawn so the reservation and the painting
// cannot disagree. The status bar is inviolable: search, command line and errors
// live in the pit.
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
	// The live marker does not ride on the range: a transcript that fits the
	// pane is still either following or pinned.
	if t.follow {
		pos = strings.TrimSpace(pos + " live")
	}
	// One footer, shared with the incipit's bookend (footerStanza).
	stanza := footerStanza(t.status, t.w, pos, t.openPit(), t.status.barVerbose())
	if len(stanza) == 0 {
		return "", []string{""}
	}
	return stanza[0], stanza[1:]
}

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

// Opening defers to a reader who has said what they want on screen; closing does not.
func (t *transcript) showQueuedAuto(on bool) {
	if on {
		if t.showing(pitQueue) {
			t.refreshQueuePit()
			return
		}
		// Dismissed by hand, or a deliberate pit is up. A note is not
		// deliberate.
		if t.queueDismissed || (t.pit.open() && !t.pit.glance()) {
			return
		}
		t.openQueuePit()
		return
	}
	t.queueDismissed = false
	if !t.showing(pitQueue) {
		return
	}
	// A drained queue closes its pit, opened by hand or not, and says so on
	// the bar.
	t.queuedByKey = false
	t.pit.close()
	t.noteQueueEmpty()
}

func firstLineTrim(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// The stale snapshot renders immediately; the async refresh replaces it in place.
func (t *transcript) openQueueFromKey() {
	// An empty queue is a sentence on the bar, not a pit; the dismissal is
	// revoked either way.
	t.queueDismissed = false
	if t.queueEmpty() {
		t.noteQueueEmpty()
		if t.queuedFetch != nil {
			// Resync anyway: the seed may still be in flight, and
			// showQueuedAuto opens the pit when a real queue lands.
			t.queuedFetch()
		}
		return
	}
	t.queuedByKey = true
	t.openQueuePit()
	if t.queuedFetch != nil {
		t.queuedFetch()
	}
}

// An unreadable queue is not empty.
func (t *transcript) queueEmpty() bool {
	return len(t.queued) == 0 && t.queuedErr == ""
}

// The pit's absence and its auto-close are the same fact, worded once.
const queueEmptyNote = "message queue is empty"

func (t *transcript) noteQueueEmpty() { t.setCommandNoteAt(queueEmptyNote, alertInfo) }

func (t *transcript) openQueuePit() {
	// No title: the bar already says "queue".
	t.pit.showList(pitQueue, "", t.queuedPitRows())
	t.focused = focusPit
}

// Keeps the selection: a queue that re-sorts under the cursor on every poll is one
// nobody can hit `x` on.
func (t *transcript) refreshQueuePit() {
	sel, had := t.pit.selected()
	keep := ""
	if had {
		keep = sel.id
	}
	t.pit.replaceRows(t.queuedPitRows(), keep)
}

// A bottom panel, not a floating overlay: the terminal has one alternate buffer.
func (t *transcript) helpLines() []string {
	body := helpBody()
	rows := make([]string, 0, len(body)+5)
	rows = append(rows, "")
	rows = append(rows, body...)
	// The mouse is documented here, not in the keymap: the table is keyed by
	// keystroke and its invariants are stated in keys.
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

// Two frame buffers are swapped so a frame does not allocate a []string of screen
// height.
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

func (t *transcript) paint(screen []string) {
	buf := append(t.paintBuf[:0], t.prefix...)
	// The alt-screen switch has reached the terminal; leave() may switch back
	// only if this happened.
	if t.altPending && t.prefix != "" {
		t.altOn, t.altPending = true, false
	}
	t.prefix = ""
	buf = append(buf, "\x1b[?2026h"...)
	// A frame that says nothing is not sent: writing the ?2026 brackets alone
	// kept a remote terminal busy.
	quiet := len(buf)
	base := t.prev
	// base == nil: the painter has no claim on the screen; a due resync
	// upgrades a frame that has something to say, and a quiet frame leaves the
	// debt owed, since re-earning the screen is a visible flash on terminals
	// without synchronized update (WSL under remote desktop). Asked up front:
	// folding it into the row loop builds a whole frame to throw away.
	full := base == nil
	if !full && t.resyncDue() {
		full = !screensEqual(screen, base)
	}
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
		// A row with no record is unknown, not blank: it must be painted.
		if r < len(base) && screen[r] == base[r] {
			continue
		}
		var old string
		if r < len(base) {
			old = base[r]
		}
		buf = appendRowUpdate(buf, r, t.w, old, screen[r])
	}
	if len(buf) == quiet {
		// Nothing to say: the resync debt stays owed until a frame differs.
		t.paintBuf = buf[:0]
		t.screenSpare, t.prev = t.prev, screen
		mark.Mark("frame.quiet", "aria", t.subjectID())
		return
	}
	buf = append(buf, "\x1b[?2026l"...)
	_, _ = t.out.Write(buf)
	t.paintBuf = buf
	t.screenSpare, t.prev = t.prev, screen
	if mark.Enabled() {
		mark.Mark("frame", "aria", t.subjectID(), "bytes", len(buf), "full", full,
			"content", t.index.total > 0, "tail", t.follow, "rows", len(screen))
	}
}

// Not fmt: the profile put 9% of paint in doPrintf for a two-digit number.
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

// mode is derived from the pit identity (pitid.go); the dispatch order is the
// order here.
func (t *transcript) mode() keyMode {
	if !t.active {
		return modeIncipit
	}
	// A box outranks the selection: the line owns the keyboard while the
	// highlight stays.
	if t.visual.active() && !t.inSearch && !t.boxOpen() {
		return modeVisual
	}
	// The drawer's sub-mode is asked after openPit: a search box or ':' line
	// over it outranks it.
	open := t.openPit()
	if open == pitComposer && t.composerMode == composerNormal {
		return modeComposer
	}
	if k := open.keys(); k == modePanel && t.listMode() {
		return modeList
	}
	return open.keys()
}

func (t *transcript) openPit() pitID {
	switch {
	case t.confirm != nil:
		return pitConfirm
	case t.inSearch:
		return pitSearch
	case t.box == boxCommand:
		return pitCommand
	case t.box == boxComposer:
		return pitComposer
	case t.pit.open():
		return t.pit.id
	default:
		return pitNothing
	}
}

// The pager never self-exits: Ctrl-D / Ctrl-C are handled at the input loop; q and
// Ctrl-T are inert here.
func (t *transcript) key(b byte) { t.dispatch(keyEvent{b: b}) }

// Up and k are one action bound twice; a nav key is never translated back into a byte.
func (t *transcript) navMotion(n navKey) { t.dispatch(keyEvent{nav: n}) }

func (t *transcript) dispatch(ev keyEvent) {
	if t.pending != "" {
		bd := t.prefixed(ev)
		t.pending = ""
		if bd != nil && bd.pager != nil {
			bd.pager(t)
			t.render()
			return
		}
	}
	switch t.mode() {
	case modeConfirm:
		// The question owns the keyboard: a key with no row must not reach
		// the pager underneath.
		if act := pagerAct.pager(modeConfirm, ev); act != nil {
			act(t)
		}
		return
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
	case modeBox:
		// Arrows are live here (history, motions); one with no row is
		// swallowed rather than scrolling the transcript.
		if ev.nav != navNone {
			if act := pagerAct.pager(modeBox, ev); act != nil {
				act(t)
				t.render()
			}
			return
		}
		if act := pagerAct.pager(modeBox, ev); act != nil {
			act(t)
		} else {
			t.boxLiteral(ev.b)
		}
		t.render()
		return
	case modeComposer:
		// The drawer owns the keyboard: a key with no row is inert.
		if act := pagerAct.pager(modeComposer, ev); act != nil {
			act(t)
			t.render()
		}
		return
	case modeVisual:
		// The selection owns the keyboard: a key with no row is inert.
		if act := pagerAct.pager(modeVisual, ev); act != nil {
			act(t)
			t.render()
		}
		return
	case modeList:
		if act := pagerAct.pager(modeList, ev); act != nil {
			act(t)
			t.render()
			return
		}
		if kv, ok := t.pit.live.(cmdkit.KeyView); ok && ev.nav == navNone && ev.b != 0 && kv.Key(ev.b) {
			t.render()
			return
		}
		t.closePanels()
	case modePanel:
		if act := pagerAct.pager(modePanel, ev); act != nil {
			act(t)
			t.render()
			return
		}
		// A hosted verb with a list: the pit owns every motion and the
		// view keeps only its own verbs; without a list the view gets
		// first refusal.
		if t.focused == focusTranscript {
			// The conversation has the keys until `T` hands them back;
			// Esc still closes the pit.
			if act := pagerAct.pager(modeTranscript, ev); act != nil {
				act(t)
				t.render()
				return
			}
		}
		if kv, ok := t.pit.live.(cmdkit.KeyView); ok && ev.nav == navNone && kv.Key(ev.b) {
			t.render()
			return
		}
		t.closePanels()
	}
	if act := pagerAct.pager(modeTranscript, ev); act != nil {
		act(t)
	}
	// A jump's report is transient: the key that set it returned from the
	// modeBox arm, so it always gets its frame.
	if !t.boxOpen() {
		t.jumpNote = ""
	}
	t.render()
}

func pagerLineDown(t *transcript) { t.scroll(1) }
func pagerLineUp(t *transcript)   { t.scrollNotch(-1) }
func pagerHalfDown(t *transcript) { t.scroll(t.h / 2) }
func pagerHalfUp(t *transcript)   { t.scroll(-(t.h / 2)) }

func pagerTail(t *transcript) {
	t.follow = true
	t.resetToTail()
}

// note is the bar's notice, not jumpNote, which the dispatcher's epilogue wipes on
// every key.
func (t *transcript) note(s string) { t.setCommandNote(s) }

func pagerTop(t *transcript) {
	t.stopFollowing()
	t.offset = 0
	t.wantTop = !t.atAriaFloor()
}

func pagerSearchPrompt(t *transcript) { t.inSearch, t.query, t.searchDir = true, "", 1 }

func pagerSearchPromptBack(t *transcript) { t.inSearch, t.query, t.searchDir = true, "", -1 }

func pagerHelpPit(t *transcript) { t.openHelpPit() }

func pagerListPit(t *transcript) { t.runListCommand("ls") }

func pagerListHome(t *transcript) { t.runListCommand("ls -H") }

func (t *transcript) runListCommand(line string) {
	if t.command == nil {
		t.note("this session cannot run commands")
		return
	}
	t.command(line)
}
func pagerFindNext(t *transcript)    { t.findRepeat(1) }
func pagerFindPrev(t *transcript)    { t.findRepeat(-1) }
func pagerHelpPanel(t *transcript)   { t.openHelpPit() }
func pagerStatusPanel(t *transcript) { t.openStatusPit() }
func pagerQueuedPanel(t *transcript) { t.openQueueFromKey() }

func pagerFormPit(t *transcript) {
	if strings.HasPrefix(string(t.pit.id), string(pitForm)) {
		t.closePanels()
		return
	}
	if t.openForm != nil {
		t.openForm()
	}
}

func pagerFocusTranscript(t *transcript) { focusTranscriptKey(t) }

// ^N/^P move the pit's selection when one is up, the node selection otherwise.
func pagerSelectNext(t *transcript) { t.selectDown(1) }
func pagerSelectPrev(t *transcript) { t.selectDown(-1) }

func (t *transcript) selectDown(dir int) {
	if p := t.pit.list(); p != nil && p.hasCursor() {
		t.pit.moveSelection(dir)
		return
	}
	t.selectNode(dir, false)
}

func pagerToggleBlocks(t *transcript) { t.toggleSelectedNodes() }

func pagerToggleBodies(t *transcript) { t.toggleSelectedTools() }

func pagerClearSelection(t *transcript) {
	if t.selection.active {
		t.clearSelection()
	}
}

func pagerVisualChar(t *transcript)     { t.pressVisual(visualChar) }
func pagerVisualLine(t *transcript)     { t.pressVisual(visualLine) }
func pagerVisualLeave(t *transcript)    { t.leaveVisual() }
func pagerVisualDown(t *transcript)     { t.visualMove(1) }
func pagerVisualUp(t *transcript)       { t.visualMove(-1) }
func pagerVisualLeft(t *transcript)     { t.visualMoveCol(-1) }
func pagerVisualRight(t *transcript)    { t.visualMoveCol(1) }
func pagerVisualHalfDown(t *transcript) { t.visualMove(t.h / 2) }
func pagerVisualHalfUp(t *transcript)   { t.visualMove(-(t.h / 2)) }
func pagerVisualEnd(t *transcript)      { t.visualJump(true) }
func pagerVisualTop(t *transcript)      { t.visualJump(false) }

func pagerVisualWordNext(t *transcript)  { t.visualWord(1, false) }
func pagerVisualWordPrev(t *transcript)  { t.visualWord(-1, false) }
func pagerVisualWordEnd(t *transcript)   { t.visualWord(1, true) }
func pagerVisualRowStart(t *transcript)  { t.visualRowStart() }
func pagerVisualFirstText(t *transcript) { t.visualFirstNonBlank() }
func pagerVisualRowEnd(t *transcript)    { t.visualRowEnd() }
func pagerVisualScreenTop(t *transcript) { t.visualScreen(-1) }
func pagerVisualScreenMid(t *transcript) { t.visualScreen(0) }
func pagerVisualScreenBot(t *transcript) { t.visualScreen(1) }
func pagerVisualParaNext(t *transcript)  { t.visualParagraph(1) }
func pagerVisualParaPrev(t *transcript)  { t.visualParagraph(-1) }

// With a highlight up the box opens holding the range placeholder; the highlight stays.
func pagerVisualCommand(t *transcript) {
	pagerCommandBox(t)
	if _, onComment := t.selectedCommentAddr(); t.visual.highlighted() || onComment {
		t.cmdline.insert(visualRangePlaceholder)
	}
}

// vim's '<,'> without the marks; expanded to the qualified coordinate at submit.
const visualRangePlaceholder = "<,>"

func (t *transcript) closePanels() {
	if t.showing(pitQueue) {
		t.queueDismissed = true
	}
	t.pit.close()
	t.queuedByKey = false
	t.focused = focusPit // the next pit opens focused
}

func (t *transcript) showing(id pitID) bool {
	return t.pit.open() && t.pit.id == id
}

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

func (t *transcript) openStatusPit() {
	rows := make([]pitRow, 0, 12)
	for _, l := range t.status.panelLines() {
		rows = append(rows, staticRow(l))
	}
	t.pit.showList(pitStatus, "", rows)
	t.focused = focusPit
}

// Esc's other meaning (clear the selection) is not reached from here.
func panelDismiss(t *transcript) { t.closePanels() }

func searchAccept(t *transcript) {
	t.inSearch = false
	t.matchQuery = t.query
	// The search targets what is open: the drawer, the list, else the conversation.
	switch {
	case t.box == boxComposer:
		t.composerMode = composerNormal
		t.composerFind(t.searchStep())
	case t.pit.open() && t.pit.list() != nil:
		if !t.pit.findRow(t.query, t.searchStep()) {
			t.setCommandNoteAt("no row matching "+t.query, alertError)
		}
	default:
		t.find(t.query)
	}
}

func searchCancel(t *transcript) { t.inSearch, t.query = false, "" }

func pagerFindRowNext(t *transcript) { t.findRow(t.searchStep()) }
func pagerFindRowPrev(t *transcript) { t.findRow(-t.searchStep()) }

func (t *transcript) findRow(dir int) {
	// The bar, not a note pit: a note is a pit and would close the list the
	// search was aimed at.
	if t.matchQuery == "" {
		t.setCommandNoteAt("no search yet", alertError)
		return
	}
	if !t.pit.findRow(t.matchQuery, dir) {
		t.setCommandNoteAt("no row matching "+t.matchQuery, alertError)
	}
}

func (t *transcript) searchTarget() string {
	switch {
	case t.box == boxComposer:
		return composerGlyph
	case t.pit.open() && t.pit.list() != nil:
		return t.pit.id.face().glyph
	}
	return ""
}

func searchBackspace(t *transcript) {
	if len(t.query) > 0 {
		t.query = t.query[:len(t.query)-1]
	}
}

// A printable key is text, not a binding.
func (t *transcript) searchLiteral(b byte) {
	if b >= 0x20 && b < 0x7f {
		t.query += string(b)
	}
}

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
	from := t.offset
	if line, ok := t.visualCursorLine(); ok {
		from = line // in visual mode the search walks from the cursor
	}
	dir := t.searchStep()
	for i := 0; i < total; i++ {
		idx := ((from+dir*(i+1))%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.landSearch(idx, q)
			return
		}
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

// stopFollowing pins the viewport first, so the jump comes after it.
func (t *transcript) landSearch(idx int, q string) {
	t.stopFollowing()
	if t.visual.active() {
		t.visualLandSearch(idx, q)
		// Keep the viewport rather than snapping the hit to the top row,
		// which would drag a highlight's other end off screen.
		t.visualEnsureVisible(idx)
		return
	}
	t.offset = idx
}

// The paged fallback walks backward: the window reaches the live tail by
// construction, so the only history to page in is older.
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
	from := t.offset
	if line, ok := t.visualCursorLine(); ok {
		from = line
	}
	delta *= t.searchStep()
	start := from + delta
	for i := 0; i < total; i++ {
		idx := ((start+delta*i)%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.landSearch(idx, q)
			return
		}
	}
	t.search = &transcriptSearch{query: q, offset: t.offset, follow: t.follow}
	t.stopFollowing()
}

func (t *transcript) searchStep() int {
	if t.searchDir < 0 {
		return -1
	}
	return 1
}

func (t *transcript) activeHighlight() string {
	if t.inSearch && t.query != "" {
		return t.query
	}
	return t.matchQuery
}

// Escapes are stepped over for matching, so coloured rows keep their colour under
// the match band.
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
	// Every retained row runs through here per frame while a search is active:
	// reject non-matches without allocating.
	if visibleIndex(row, q) < 0 {
		return row
	}
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
						continue
					}
					if searchContains(t.lineAt(i), q) {
						t.offset, t.follow = i, false
						t.visualLandSearch(i, q)
						return true
					}
				}
				return false
			}
		}
	}
	return false
}

// visibleIndex matches q against the visible text, escapes skipped and allowed to
// interrupt the match, without allocating.
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
		strings.Contains(coordLabel(m.Turn, inquiryNode, 0, t.coordFormat()), q) {
		return true
	}
	for i, n := range m.Nodes {
		if markdownMayRenderQuery(n.Markdown, q) || strings.Contains(n.Name, q) ||
			strings.Contains(n.Summary, q) || strings.Contains(n.Output, q) {
			return true
		}
		if verbose && strings.Contains(coordLabel(m.Turn, int(m.From)+i, nodeCoordAt(n), t.coordFormat()), q) {
			return true
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

// Only the viewport goes home: the floor the walk reached is honest, and restoring
// a higher one would re-fetch on the next scroll.
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

// A sentinel about to come on screen is a fetch already late: the same prefetch
// distance the floor uses.
func (t *transcript) gapNear() *aria.Gap {
	t.buildIndex()
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

// whole: no hole inside the window and nothing below its floor; the footer's total
// must not claim to be the conversation's size.
func (t *transcript) whole() bool {
	for k := range t.index.entries {
		if t.index.entries[k].isGap() {
			return false
		}
	}
	return t.atAriaFloor()
}

// A tall turn is several slices, so one toggle can touch more than one.
func (t *transcript) dropTurnRows(lt int) {
	t.dropTurnsRows(map[int]struct{}{lt: {}})
}

// The cache is keyed by slice, so a per-turn call is a full scan; batch.
func (t *transcript) dropTurnsRows(lts map[int]struct{}) {
	if len(lts) == 0 {
		return
	}
	t.rowRev++
	for k := range t.rowCache {
		if _, ok := lts[k.turn()]; ok {
			delete(t.rowCache, k)
		}
	}
	for k := range t.stickyCache {
		if _, ok := lts[k.turn()]; ok {
			delete(t.stickyCache, k)
		}
	}
}

// base is the first turn the two arias do not share: below it rows, question
// blocks and the window are kept. The reader's posture (pane, panels, verbose) is
// kept either way. Called with the render lock held.
func (t *transcript) retarget(client *aria.Client, figaroID string, status *sessionStatus, base int) {
	t.client = client
	t.setStatus(status)
	keepScroll := t.keepsScroll(base)
	t.kept = keepScroll
	// Fresh maps, always: the old ones may be on the shelf (subject_park).
	t.rowCache = keepRowsBelow(t.rowCache, base)
	t.stickyCache = keepStickyBelow(t.stickyCache, base)
	t.expanded = keepRefsBelow(t.expanded, base)
	t.adorned = keepRefsBelow(t.adorned, base)
	if !keepScroll {
		t.from = aria.Anchor{}
		t.offset = 0
		t.follow = true
	}
	t.tailTuned, t.tailWant = false, 0
	// Turn 1 of the new aria has the key turn 1 of the old one had, so every
	// cache under it goes by hand, the sticky header included.
	t.openMemo = openMemo{}
	// A cue in the shared prefix points at the same node in both arias.
	if !t.selection.below(base) {
		t.selection = nodeSelection{}
	}
	if !t.visual.below(base) {
		t.visual = visualSelection{}
	}
	t.index = lineIndex{}
	t.lineKey = t.lineKey[:0]
	t.frameRefs = t.frameRefs[:0]
	t.invalidateWindow()

	// A walk or search aimed at the old aria's coordinates must not survive.
	t.jump, t.jumpNote = nil, ""
	t.search = nil
	t.inSearch, t.query, t.matchQuery = false, "", ""
	t.searchDir = 1
	t.pending = ""

	// Repainted whole: prev describes a conversation no longer on screen.
	t.prev = nil
	if t.active {
		t.client.SetClosedLimit(0) // the pager owns retention while it is up
	}
}

// If the top of the window is inside the shared prefix every row to the divergence
// is the same bytes, so the screen stays; otherwise go to the live tail. Following
// is itself a position.
func (t *transcript) keepsScroll(base int) bool {
	if base <= 0 || t.follow {
		return false
	}
	i := t.index.entryAt(t.offset)
	if i < 0 {
		return false
	}
	e := &t.index.entries[i]
	return !e.isGap() && e.turn > 0 && e.turn < base
}

// Keep below base, drop at or above, into a new map; a fold left over from the
// other aria would open a block of this one.
func keepRowsBelow(src map[sliceKey]cachedMessage, base int) map[sliceKey]cachedMessage {
	out := make(map[sliceKey]cachedMessage, len(src))
	for k, v := range src {
		if k.turn() < base {
			out[k] = v
		}
	}
	return out
}

func keepStickyBelow(src map[sliceKey]stickyQuestion, base int) map[sliceKey]stickyQuestion {
	out := make(map[sliceKey]stickyQuestion, len(src))
	for k, v := range src {
		if k.turn() < base {
			out[k] = v
		}
	}
	return out
}

func keepRefsBelow(src map[nodeRef]bool, base int) map[nodeRef]bool {
	out := make(map[nodeRef]bool, len(src))
	for r, v := range src {
		if r.turn < base {
			out[r] = v
		}
	}
	return out
}

func (t *transcript) inputDrawerLines() []string {
	var rows []string
	switch {
	case t.inSearch:
		sigil := "/"
		if t.searchStep() < 0 {
			sigil = "?"
		}
		// A search aimed at a drawer or list says so with one glyph.
		if g := t.searchTarget(); g != "" {
			sigil = g + sigil
		}
		rows = []string{pitGray(clipToWidth(sigil+t.query, t.w))}
	case t.box != boxNone:
		// The prompt is the editor's: under ^R it reads
		// `(reverse-i-search)`. The box wraps on the pit's page; the
		// drawer may take the pane (F), the command line may not.
		e := t.editor()
		rows = t.composerTitle()
		room := max(min(t.boxRows(), t.pitRoom())-len(rows), 1)
		if t.fullPit() {
			room = max(t.pitRoom()-len(rows), 1)
		}
		lo, hi := -1, -1
		if from, to, ok := t.composerSpan(); ok {
			lo, hi = from, to
		}
		rows = append(rows, e.wrapWash(e.prompt(t.boxSigil()), t.w, room, lo, hi)...)
		if t.box == boxComposer && t.fullPit() {
			for len(rows) < room {
				rows = append(rows, "")
			}
		}
	default:
		return nil
	}
	footer, own := t.jumpFooter()
	if t.boxOpen() && t.menu != nil {
		room := t.pitRoom() - len(rows)
		if own {
			room--
		}
		rows = append(rows, t.menu.lines(t.w, room)...)
	}
	if own {
		rows = append(rows, pitGray(clipToWidth("  "+footer, t.w)))
	}
	return rows
}

// Opening it is reading it, so the unread mark goes.
func (t *transcript) openNotificationsPit() {
	t.pit.showLive(pitNotifications, &notificationsView{store: t.notes})
	t.focused = focusPit
	t.notes.markRead()
}

func pagerNotifications(t *transcript) { t.togglePit(pitNotifications, t.openNotificationsPit) }

// prefixed is the binding a half-typed gesture gives this key: in the mode
// the pager is in, or in the transcript's, which is where every mode that
// does not own a key sends it.
func (t *transcript) prefixed(ev keyEvent) *keyBinding {
	if bd := prefixBinding(t.pending, t.mode(), ev); bd != nil {
		return bd
	}
	return prefixBinding(t.pending, modeTranscript, ev)
}

func pagerClearNote(t *transcript) { t.note("") }

func (t *transcript) showLivePit(name string, v cmdkit.LiveView, full bool) {
	t.queuedByKey = false
	t.pit.showLive(pitID(name), v)
	if full {
		t.full = true
	}
	t.focused = focusPit
	t.render()
}

// A one-line result is an alert on the bar, retiring after cli.notice_ttl;
// anything longer is a pit. A note must never replace an open list.
func (t *transcript) setCommandNote(note string) { t.setCommandNoteAt(note, alertInfo) }

// The bar alone, not posted to the history: the result follows and replaces it.
func (t *transcript) setProgressNote(note string) {
	if note == "" || strings.Contains(note, "\n") {
		return
	}
	t.status.setProgress(note)
	t.render()
}

func (t *transcript) setCommandNoteAt(note string, level alertLevel) {
	// Every note is posted to the history, whatever the bar does with it.
	t.notes.post(level, "cli", note)
	if note == "" {
		t.status.setNotice("")
		if t.showing(pitNote) {
			t.pit.close()
		}
		t.render()
		return
	}
	if !strings.Contains(note, "\n") {
		t.status.setNoticeAt(note, level)
		t.render()
		return
	}
	t.pit.showNote(note)
	t.focused = focusPit
	t.render()
}

// In the bar, never a pit: a confirmation must not replace the list it was yanking
// from.
func (t *transcript) noteYank(text string) {
	one := firstLineTrim(text)
	if displayWidth(one) > 24 {
		one = fmt.Sprintf("%d bytes", len(text))
	}
	t.setCommandNote("yanked " + one)
}

// The transcript does not know what dropping means; it hands the id up, as the ':'
// box hands a line up.
func pagerPitDrop(t *transcript) { t.dropSelected(true) }

func pagerPitDropNow(t *transcript) { t.dropSelected(false) }

func (t *transcript) dropRowAt(pit, id, next string) {
	if t.dropRow != nil {
		t.dropRow(pit, id, next)
	}
}

func (t *transcript) dropSelected(ask bool) {
	if t.pit.id == pitQueue {
		t.dropQueuedSelected()
		return
	}
	row, ok := t.pit.selected()
	if !ok || row.id == "" || t.dropRow == nil {
		return
	}
	pit, id := string(t.pit.id), row.id
	// Where the reader stands afterwards is a fact about this list, not the daemon.
	next := ""
	if n, ok := t.pit.neighbour(); ok {
		next = n.id
	}
	drop := func() {
		t.dropRowAt(pit, id, next)
		t.pit.removeSelected() // optimistic: the refresh confirms it
	}
	// Only an aria is worth a question: a queued message is reprinted on the
	// way out, an aria is gone.
	if ask && rpc.ValidateAriaID(id) == nil {
		t.askConfirm(killPrompt(id), drop)
		return
	}
	drop()
}

type openMemo struct {
	key    sliceKey
	w      int
	blocks map[int]*memoBlock
}

type memoBlock struct {
	node   livedoc.Node
	state  ldrender.BlockState
	rows   []ldrender.Row
	out    []transcriptRow // clipped, collapsed, addressed
	reused bool
}

func (mo *openMemo) compose(block int, n livedoc.Node, state ldrender.BlockState, draw func() []ldrender.Row) []ldrender.Row {
	// A running tool is drawn against the clock: its spinner is a function of
	// the frame counter, so it is recomposed every frame.
	animating := n.Type == livedoc.NodeTool && n.Status != livedoc.StatusOK && n.Status != livedoc.StatusError
	if b, ok := mo.blocks[block]; ok {
		if !animating && b.state == state && reflect.DeepEqual(b.node, n) {
			b.reused = true
			return b.rows
		}
	}
	rows := draw()
	mo.blocks[block] = &memoBlock{node: n, state: state, rows: rows}
	return rows
}

// The clip/collapse/address pass costs as much as the composition did, so it is
// memoized too.
func (mo *openMemo) kept(block int) ([]transcriptRow, bool) {
	b, ok := mo.blocks[block]
	if !ok || !b.reused || b.out == nil {
		return nil, false
	}
	return b.out, true
}

func (mo *openMemo) keep(block int, out []transcriptRow) {
	if b, ok := mo.blocks[block]; ok {
		b.out = out
	}
}
