package cli

import (
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"strconv"
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

	prev   []string // last painted screen (full-frame diff)
	lineLT []int    // LT owning each line of lines(), for resize anchoring
	offset int      // top line of the viewport into lines()
	follow bool     // stick to the bottom on new content
	pendG  bool     // saw one 'g' (for gg)

	inSearch   bool
	query      string
	matchQuery string // persistent query: highlights + n/N target

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
	heldOpen    *aria.Message
	committedW  int

	// rowCache memoizes rows of committed messages in their unselected resting
	// form — clipped and gutter-prefixed (plainNodeRow), but carrying no
	// selection cue and no search highlight. That keeps the cache a pure
	// function of (message, width), so selection and search state can change
	// without invalidating a single cached row; the cue and the highlight are
	// applied per painted row by window()/entryLine().
	rowCache  map[int]cachedMessage
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
	lineBuf     []string     // whole-window rows, valid until the next lines()
	keepBuf     map[int]bool // reused live-LT set for pruneCaches
	paintBuf    []byte       // reused escape-sequence output buffer
	screenSpare []string     // the frame buffer displaced by the last paint
}

type transcriptPage struct {
	desc     pageDesc
	messages []aria.Message
}

// pageDesc is sufficient to replay and verify an evicted immutable page.
type pageDesc struct {
	FirstLT      int
	LastLT       int
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
	before    int
	direction transcriptPageDirection
	expected  pageDesc
	after     int
	watermark int
	cached    []aria.Message
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	return &transcript{
		out: out, view: view, client: client,
		status: newSessionStatus(figaroID, startedAt), w: w, h: h,
		rowCache: map[int]cachedMessage{}, expanded: map[nodeRef]bool{},
	}
}

// enter switches to the alternate screen and draws the transcript at the bottom.
// autowrapOff is asserted explicitly here (not just inherited from the caller):
// this is a fixed-canvas pager, and a single wide glyph tipping the bottom-right
// cell past the last column with autowrap ON scrolls the whole screen up by a
// row — which reads as the status line "eating" the line above it.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query, t.matchQuery = false, false, "", ""
	t.resetToTail()
	io.WriteString(t.out, altScreenOn+autowrapOff+ldmouse.Enable+cursorHide+"\x1b[2J")
	t.render()
}

// leave restores the normal screen. Mouse reporting is disabled before the
// alt-screen swap so no stray \x1b[<…M leaks into the shell.
func (t *transcript) leave() {
	t.active = false
	io.WriteString(t.out, "\x1b[2J"+ldmouse.Disable+altScreenOff)
	t.prev = nil
}

// scrollBy moves the viewport by delta lines (native wheel), leaving follow
// mode; render clamps the offset.
func (t *transcript) scrollBy(delta int) {
	if !t.active {
		return
	}
	t.offset += delta
	t.stopFollowing()
	if delta < 0 {
		t.checkOlder = true // scrolled up: maybe page older history
	} else if delta > 0 {
		t.checkNewer = true
	}
	t.render()
}

// transcriptPageSize is how many older messages a single scroll-up fetch pulls.
const (
	transcriptPageSize        = 30
	transcriptPageLimit       = 3
	transcriptTailLimit       = 2 * transcriptPageSize
	transcriptDescLimit       = 64
	transcriptPayloadLRULimit = 3
)

func (t *transcript) pageCursor() (transcriptPageRequest, bool) {
	if t.checkOlder && t.noMoreOlder {
		t.checkOlder = false
	}
	if t.checkOlder && !t.noMoreOlder {
		t.checkOlder = false
		if t.search == nil && t.offset >= t.h {
			return transcriptPageRequest{}, false
		}
		oldest, ok := t.oldestLT()
		if !ok {
			return transcriptPageRequest{}, false
		}
		if oldest <= 1 {
			t.noMoreOlder = true
			t.finishSearch(false)
			t.render()
			return transcriptPageRequest{}, false
		}
		return transcriptPageRequest{before: oldest, direction: pageOlder}, true
	}
	if t.checkNewer && len(t.newer) > 0 {
		t.checkNewer = false
		if t.search == nil && t.offset+t.h < len(t.lineLT) {
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
			t.render()
		} else if t.search != nil {
			t.wrapSearchOlder()
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
	if t.heldOpen != nil && t.heldOpen.LT >= desc.FirstLT && t.heldOpen.LT <= desc.LastLT {
		t.heldOpen = nil
	}
	searching := t.search != nil
	anchorLT, within := 0, 0
	if !searching {
		anchorLT, within = t.viewportAnchor()
	}
	page := transcriptPage{desc: desc, messages: messages}
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
		t.restoreViewportAnchor(anchorLT, within)
	}
	t.render()
}

func committedMessages(in []aria.Committed) []aria.Message {
	messages := make([]aria.Message, 0, len(in))
	for _, m := range in {
		if m.Full() {
			messages = append(messages, aria.Message{LT: m.LT, Role: m.Role, Nodes: m.Nodes})
		}
	}
	return messages
}

func (t *transcript) trimPages(direction transcriptPageDirection) {
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

func (t *transcript) dropPage(page transcriptPage) {
	for _, m := range page.messages {
		delete(t.rowCache, m.LT)
		for ref := range t.expanded {
			if ref.lt == m.LT {
				delete(t.expanded, ref)
			}
		}
	}
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
		copy(t.payloadLRU, t.payloadLRU[len(t.payloadLRU)-transcriptPayloadLRULimit:])
		clear(t.payloadLRU[transcriptPayloadLRULimit:])
		t.payloadLRU = t.payloadLRU[:transcriptPayloadLRULimit]
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

func (t *transcript) resetToTail() {
	v := t.client.View()
	closed := v.Closed
	if len(closed) > transcriptPageSize {
		closed = closed[len(closed)-transcriptPageSize:]
	}
	t.pages = nil
	if len(closed) > 0 {
		t.pages = []transcriptPage{{desc: describePage(closed), messages: closed}}
		if closed[len(closed)-1].LT > t.committedW {
			t.committedW = closed[len(closed)-1].LT
		}
	}
	t.newer = nil
	t.payloadLRU = nil
	t.checkNewer = false
	t.heldOpen = nil
	t.noMoreOlder = len(closed) > 0 && closed[0].LT <= 1
	t.pruneCaches()
}

func (t *transcript) pruneCaches() {
	// Called from resetToTail, i.e. once per frame while following the live
	// tail, so the LT set is reused rather than rebuilt.
	keep := t.keepBuf
	if keep == nil {
		keep = make(map[int]bool, transcriptPageSize*transcriptPageLimit)
		t.keepBuf = keep
	} else {
		clear(keep)
	}
	t.forEachMessage(func(m aria.Message) { keep[m.LT] = true })
	for lt := range t.rowCache {
		if !keep[lt] {
			delete(t.rowCache, lt)
		}
	}
	for ref := range t.expanded {
		if !keep[ref.lt] {
			delete(t.expanded, ref)
		}
	}
}

// forEachMessage walks the retained window without materializing the merged
// slice messages() returns — it is called from the frame path, where one
// allocation and a copy of every retained message header per frame is pure
// waste.
func (t *transcript) forEachMessage(fn func(aria.Message)) {
	for _, page := range t.pages {
		for _, m := range page.messages {
			fn(m)
		}
	}
}

func (t *transcript) messages() []aria.Message {
	n := 0
	for _, page := range t.pages {
		n += len(page.messages)
	}
	out := make([]aria.Message, 0, n)
	for _, page := range t.pages {
		out = append(out, page.messages...)
	}
	return out
}

func (t *transcript) oldestLT() (int, bool) {
	for _, page := range t.pages {
		if len(page.messages) > 0 {
			return page.messages[0].LT, true
		}
	}
	return 0, false
}

func (t *transcript) newestLT() (int, bool) {
	for i := len(t.pages) - 1; i >= 0; i-- {
		if n := len(t.pages[i].messages); n > 0 {
			return t.pages[i].messages[n-1].LT, true
		}
	}
	return 0, false
}

func (t *transcript) hasNewerHistory() bool {
	if len(t.newer) > 0 {
		return true
	}
	newest, ok := t.newestLT()
	return ok && newest < t.committedW
}

func (t *transcript) observeCommitted(m aria.Message) {
	if m.LT > t.committedW {
		t.committedW = m.LT
	}
	if t.heldOpen != nil && t.heldOpen.LT == m.LT {
		copy := m
		copy.Nodes = append([]livedoc.Node(nil), m.Nodes...)
		t.heldOpen = &copy
	}
}

func describePage(messages []aria.Message) pageDesc {
	if len(messages) == 0 {
		return pageDesc{}
	}
	h := fnv.New64a()
	var b [8]byte
	for _, m := range messages {
		v := uint64(m.LT)
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	last := messages[len(messages)-1].LT
	return pageDesc{
		FirstLT: messages[0].LT, LastLT: last, Count: len(messages),
		ReplayBefore: last + 1, LTHash: h.Sum64(),
	}
}

func (d pageDesc) equal(other pageDesc) bool {
	return d.FirstLT == other.FirstLT && d.LastLT == other.LastLT &&
		d.Count == other.Count && d.ReplayBefore == other.ReplayBefore &&
		d.LTHash == other.LTHash
}

func (t *transcript) resize(w, h int) {
	// Anchor on the message at the viewport top: a width change re-wraps rows and
	// changes line counts, so keeping the raw line offset would jump the view.
	// Record the top message's LT + how many lines into it we are, then restore
	// after re-rendering at the new width. (Skipped when following the tail.)
	anchorLT, within := t.viewportAnchor()
	t.w, t.h = w, h
	t.prev = nil   // full repaint (diff vs nil); no \x1b[2J, which flickers
	t.buildIndex() // re-render at the new width, repopulating lineLT
	t.restoreViewportAnchor(anchorLT, within)
	t.render()
}

func (t *transcript) viewportAnchor() (int, int) {
	if t.follow || t.offset >= len(t.lineLT) {
		return 0, 0
	}
	lt := t.lineLT[t.offset]
	start := t.offset
	for start > 0 && t.lineLT[start-1] == lt {
		start--
	}
	return lt, t.offset - start
}

func (t *transcript) restoreViewportAnchor(lt, within int) {
	if lt == 0 {
		return
	}
	for i, lineLT := range t.lineLT {
		if lineLT == lt {
			t.offset = i + within
			return
		}
	}
}

func (t *transcript) invalidateRows() {
	t.rowCache = map[int]cachedMessage{}
}

// lines renders the retained message window and live tail to physical rows.
// Committed messages are immutable, so their rendered rows are cached by LT;
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

func (t *transcript) openMessage() *aria.Message {
	if !t.follow {
		return t.heldOpen
	}
	return t.client.View().Open
}

func (t *transcript) stopFollowing() {
	if t.follow {
		t.heldOpen = t.client.View().Open
	}
	t.follow = false
}

// renderMsgBase renders one message without selection decoration. Committed
// instances are cached; open messages are rebuilt on every live frame.
func (t *transcript) renderMsgBase(m aria.Message) cachedMessage {
	var rows []transcriptRow
	if h := messageHeader(m.Role); h != "" {
		rows = append(rows, transcriptRow{text: h}, transcriptRow{})
	}
	for k, n := range m.Nodes {
		if k > 0 {
			rows = append(rows, transcriptRow{})
		}
		ref := nodeRef{lt: m.LT, index: k}
		for _, l := range t.renderNode(n, ref) {
			// Rows are stored already clipped and gutter-prefixed (their
			// unselected resting form) so a frame that touches nothing
			// allocates nothing; see plainNodeRow.
			rows = append(rows, transcriptRow{text: plainNodeRow(l, t.w), ref: ref})
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
	t.buildIndex()
	total := t.index.total
	foot := []string{}
	if t.showHelp {
		foot = t.helpLines()
	} else if t.showStatus {
		foot = t.statusPanelLines()
	} else if t.showQueued {
		foot = t.queuedPanelLines()
	}
	body := t.h - 3 - len(foot) // bottom rows: panel (if open) + rule + blank + status
	if body < 1 {
		body = 1
	}
	maxOff := total - body
	if maxOff < 0 {
		maxOff = 0
	}
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
		if r := body + k; r < t.h-3 {
			screen[r] = l
		}
	}
	rule, status := t.footerRows(total, body)
	screen[t.h-3] = "" // padding ABOVE the footer, not between rule and status
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
		if t.follow {
			pos += " live"
		}
	}
	rule = "\x1b[2m" + t.status.ruleLine(t.w, pos) + "\x1b[0m"
	if t.inSearch {
		return rule, "\x1b[2m" + clipToWidth("/"+t.query, t.w) + "\x1b[0m"
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
func (t *transcript) helpLines() []string {
	rows := []string{
		"",
		"  j/k · u/d · gg/G    scroll · half-page · top/bottom",
		"  /                   search (Enter jump · Esc cancel typing)",
		"  n / N               next / previous match",
		"  y                   copy selection (or aria id if none)",
		"  ^O                  toggle verbose tool output",
		"  ^N/^P               select next/previous node",
		"  ^N/^P + Shift       extend node selection (Alt+^N/^P fallback)",
		"  Enter               expand tools within the selection",
		"  ^C                  copy selected node(s) / interrupt turn",
		"  Esc                 clear selection / close panel",
		"  ^L                  listen — stay open after the turn ends",
		"  q / ^D              detach; the turn keeps running",
		"  !                   figaro status panel",
		"  Q                   queued prompts panel",
		"  ?                   close help",
	}
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

// paint writes the diff between the composed frame and the last one. The
// output buffer is reused across frames: a scroll dirties every row, and on
// a wide terminal full of styled rows that is tens of kilobytes of escape
// bytes per keypress — previously grown from nothing (and formatted through
// fmt) on every single frame.
func (t *transcript) paint(screen []string) {
	b := t.paintBuf[:0]
	b = append(b, "\x1b[?2026h"...)
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(t.prev) {
			old = t.prev[r]
		}
		if screen[r] != old {
			b = append(b, "\x1b["...)
			b = strconv.AppendInt(b, int64(r+1), 10)
			b = append(b, ";1H\x1b[2K"...)
			b = append(b, screen[r]...)
		}
	}
	b = append(b, "\x1b[?2026l"...)
	t.paintBuf = b
	t.out.Write(b)
	t.screenSpare, t.prev = t.prev, screen
}

// key handles one navigation/search input byte. Transcript is a locked mode:
// keys only scroll or search — it NEVER self-exits. Exit is Ctrl-D / Ctrl-C,
// handled at the input loop. q, Esc, and Ctrl-T are deliberately inert here.
func (t *transcript) key(b byte) {
	if t.inSearch {
		t.searchKey(b)
		t.render()
		return
	}
	if t.showHelp || t.showStatus || t.showQueued { // any key wipes the panel; nav keys also still act below
		reopen := byte(0)
		switch {
		case t.showHelp && b == '!':
			reopen = '!' // switch panels directly
		case t.showHelp && b == 'Q':
			reopen = 'Q'
		case t.showStatus && b == '?':
			reopen = '?'
		case t.showStatus && b == 'Q':
			reopen = 'Q'
		case t.showQueued && b == '?':
			reopen = '?'
		case t.showQueued && b == '!':
			reopen = '!'
		}
		t.showHelp, t.showStatus, t.showQueued = false, false, false
		switch {
		case reopen == '!':
			t.showStatus = true
		case reopen == '?':
			t.showHelp = true
		case reopen == 'Q':
			t.openQueuedPanel()
			t.render()
			return
		case b == '?' || b == '!' || b == 'Q' || b == 0x1b:
			t.render()
			return
		}
		if reopen != 0 {
			t.render()
			return
		}
	}
	switch b {
	case 'j':
		t.offset++
		t.stopFollowing()
		t.checkNewer = true
	case 'k':
		t.offset--
		t.stopFollowing()
		t.checkOlder = true
	case 'd':
		t.offset += t.h / 2
		t.stopFollowing()
		t.checkNewer = true
	case 'u':
		t.offset -= t.h / 2
		t.stopFollowing()
		t.checkOlder = true
	case 'G':
		t.follow = true
		t.resetToTail()
	case 'g':
		if t.pendG {
			t.offset = 0
			t.stopFollowing()
			t.checkOlder = true
		}
	case '/':
		t.inSearch, t.query = true, ""
	case 'n':
		t.findRepeat(1)
	case 'N':
		t.findRepeat(-1)
	case '?':
		t.showHelp = true
	case '!':
		t.showStatus = true
	case 'Q':
		t.openQueuedPanel()
	case 0x0e: // Ctrl-N
		t.selectNode(1, false)
	case 0x10: // Ctrl-P
		t.selectNode(-1, false)
	case 0x0d, 0x0a:
		t.toggleSelectedTools()
	case 0x1b: // Esc: clear the active selection (no-op otherwise)
		if t.selection.active {
			t.clearSelection()
		}
	}
	t.pendG = b == 'g' && !t.pendG
	t.render()
}

func (t *transcript) searchKey(b byte) {
	switch b {
	case 0x0d, 0x0a: // Enter → jump to first match
		t.inSearch = false
		t.matchQuery = t.query
		t.find(t.query)
	case 0x1b: // Esc → cancel typing (keeps existing highlights)
		t.inSearch, t.query = false, ""
	case 0x7f, 0x08: // backspace
		if len(t.query) > 0 {
			t.query = t.query[:len(t.query)-1]
		}
	default:
		if b >= 0x20 && b < 0x7f {
			t.query += string(b)
		}
	}
}

// find scrolls to the first line at/after the cursor containing q (wrapping).
func (t *transcript) find(q string) {
	if q == "" {
		return
	}
	t.matchQuery = q
	t.buildIndex()
	total := t.index.total
	if total == 0 {
		return
	}
	for i := 0; i < total; i++ {
		idx := (t.offset + 1 + i) % total
		if searchContains(t.lineAt(idx), q) {
			t.offset = idx
			t.stopFollowing()
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
	t.buildIndex()
	total := t.index.total
	if total == 0 {
		return
	}
	start := t.offset + delta
	for i := 0; i < total; i++ {
		idx := ((start+delta*i)%total + total) % total
		if searchContains(t.lineAt(idx), q) {
			t.offset = idx
			t.stopFollowing()
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
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[m.LT] = rows
		}
		for _, row := range rows.rows {
			// searchText strips the gutter column plainNodeRow baked in.
			if searchContains(row.searchText(), q) {
				t.buildIndex()
				for i := range t.index.total {
					if t.lineLT[i] != m.LT {
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
	verbose := false
	if view, ok := t.view.(*ariaView); ok && view.settings != nil {
		verbose = view.settings.verbose
	}
	for i, n := range m.Nodes {
		if markdownMayRenderQuery(n.Markdown, q) || strings.Contains(n.Name, q) ||
			strings.Contains(n.Summary, q) || strings.Contains(n.Output, q) {
			return true
		}
		if n.Type == livedoc.NodeSteering && strings.Contains("↳ you", q) {
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
		if !t.expanded[nodeRef{lt: m.LT, index: i}] && n.Output != "" {
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
