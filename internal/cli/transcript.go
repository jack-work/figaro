package cli

import (
	"io"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
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
	now         func() time.Time
	lastFull    time.Time
	trustScreen bool
	lineKey     []sliceKey // slice owning each line, for resize anchoring
	offset      int        // top line of the viewport
	// wantTop is a standing request for the beginning, armed by Home/gg.
	wantTop bool
	kept    bool
	follow  bool
	pending string

	comments  *commentIndex
	entryOpen map[nodeRef]bool

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
	metaOpen  map[nodeRef]bool

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

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	t := &transcript{
		out: out, view: view, client: client, w: w, h: h,
		rowCache: map[sliceKey]cachedMessage{}, stickyCache: map[sliceKey]stickyQuestion{},
		expanded: map[nodeRef]bool{}, metaOpen: map[nodeRef]bool{},
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

func (t *transcript) subjectID() string {
	if t.status == nil {
		return ""
	}
	return t.status.figaroID
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
	t.metaOpen = keepRefsBelow(t.metaOpen, base)
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
