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
// Keys: j/k line, u/d half-page, gg/G top/bottom, / literal search, ? help
// panel. Exit is Ctrl-D/Ctrl-C at the input loop. Not safe for concurrent use;
// the caller serializes all entry points.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client
	status *sessionStatus

	active     bool
	showHelp   bool // '?': the footer grows into a key-reference panel
	showStatus bool // '!': the footer grows into the figaro-status panel
	w, h       int
	tick       int

	prev   []string // last painted screen (full-frame diff)
	lineLT []int    // LT owning each line of lines(), for resize anchoring
	offset int      // top line of the viewport into lines()
	follow bool     // stick to the bottom on new content
	pendG  bool     // saw one 'g' (for gg)

	inSearch     bool
	query        string
	promptFilter bool           // the open prompt is '&' (filter), not '/' or '?'
	promptBack   bool           // the open prompt is '?' (backward search)
	searchBack   bool           // committed search direction: n follows it, N reverses
	searchErr    string         // last bad-pattern error, shown in the prompt row
	pattern      *searchPattern // active search: highlight-all + n/N until Esc
	filter       *searchPattern // active '&' filter: only matching rows render

	// Visual selection (vim v/V/^V) over the rendered rows. The cursor is
	// also where n/N land, so search drives selection endpoints. Endpoints
	// are LT-anchored (message LT + line-within + column) because raw row
	// indices shift whenever history folds in (live frames, listen catch-up,
	// paging); they resolve against the current lines() at every use.
	vmode      visualMode
	vAnchor    visualPoint
	vCursor    visualPoint
	hasCursor  bool
	statsCache matchStatsCache

	// lines() memo: rebuilt only when something that feeds it changed (a
	// keypress, a model frame, a tick, a resize). The open message renders
	// through the full markdown pipeline, so uncached repeat calls within
	// one event dominated large-window profiles.
	linesDirty bool
	linesMemo  []string
	linesGen   uint64 // client.Gen() the memo was built against

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

	// rowCache memoizes unstyled rows of committed messages. Selection is
	// applied after retrieval, so moving through nodes never re-renders prose.
	rowCache  map[int]cachedMessage
	cacheW    int
	nodeRows  map[nodeRef]nodeSpan
	selection nodeSelection
	expanded  map[nodeRef]bool
}

type matchStatsKey struct {
	src  string
	rows int
}

type matchPos struct{ row, col int }

type matchStatsCache struct {
	key       matchStatsKey
	positions []matchPos
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
	wrapWindow  bool // n/N: on directional exhaust, wrap within the window
	backward    bool // findNext(-1): older pages land on their LAST match
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
		rowCache: map[int]cachedMessage{}, nodeRows: map[nodeRef]nodeSpan{}, expanded: map[nodeRef]bool{},
	}
}

// enter switches to the alternate screen and draws the transcript at the bottom.
// autowrapOff is asserted explicitly here (not just inherited from the caller):
// this is a fixed-canvas pager, and a single wide glyph tipping the bottom-right
// cell past the last column with autowrap ON scrolls the whole screen up by a
// row — which reads as the status line "eating" the line above it.
func (t *transcript) enter() {
	t.markDirty()
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query = false, false, ""
	t.pattern, t.filter, t.promptFilter, t.searchErr = nil, nil, false, ""
	t.vmode, t.hasCursor = visualOff, false
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
	t.markDirty()
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
	t.markDirty()
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
		if t.findPage(t.pattern, messages, t.search.backward) {
			t.search = nil
		} else if t.search.direction == pageNewer {
			if t.hasNewerHistory() {
				t.checkNewer = true
			} else if t.search.wrapWindow {
				t.finishSearch(false) // n: newer exhausted → wrap in-window
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
		t.lines()
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
	keep := make(map[int]bool)
	for _, m := range t.messages() {
		keep[m.LT] = true
	}
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
	t.markDirty()
	t.prev = nil // full repaint (diff vs nil); no \x1b[2J, which flickers
	t.lines()    // re-render at the new width, repopulating lineLT
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

// markDirty invalidates the lines() memo; every mutation source calls it.
func (t *transcript) markDirty() { t.linesDirty = true }

func (t *transcript) invalidateRows() {
	t.markDirty()
	t.rowCache = map[int]cachedMessage{}
}

// lines renders the retained message window and live tail to physical rows.
// Committed messages are immutable, so their rendered rows are cached by LT;
// only the open message renders every frame.
func (t *transcript) lines() []string {
	if !t.linesDirty && t.linesMemo != nil && t.client.Gen() == t.linesGen {
		return t.linesMemo
	}
	if t.follow {
		t.resetToTail()
	}
	if t.cacheW != t.w { // width changed: cached rows are stale
		t.rowCache = map[int]cachedMessage{}
		t.cacheW = t.w
	}
	marks := t.selectionMarks()
	var out []string
	var lts []int // LT owning each line (0 for separator rules), parallel to out
	t.nodeRows = map[nodeRef]nodeSpan{}
	appendMsg := func(rows []transcriptRow, lt int) {
		if t.filter != nil {
			// '&' filter: a readonly g/re/p view — matching rows only, no
			// separators or blanks, no node spans (selection is meaningless
			// over a discontiguous slice of the conversation).
			for _, r := range rows {
				if !t.filter.match(r.text) {
					continue
				}
				out = append(out, r.text)
				lts = append(lts, lt)
			}
			return
		}
		if len(out) > 0 { // rule separator BETWEEN messages only — the footer
			out = append(out, "", dimTransRule(t.w), "") // seals the last one, so a
			lts = append(lts, lt, lt, lt)                // trailing rule+blank would
		} // double up against it
		for _, r := range rows {
			line := r.text
			if r.ref.valid() {
				line = decorateNodeRow(line, marks[r.ref], t.w)
				span, ok := t.nodeRows[r.ref]
				if !ok {
					span.first = len(out)
				}
				span.last = len(out)
				t.nodeRows[r.ref] = span
			}
			out = append(out, line)
			lts = append(lts, lt)
		}
	}
	for _, m := range t.messages() {
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[m.LT] = rows
		}
		appendMsg(rows.rows, m.LT)
	}
	if open := t.openMessage(); open != nil {
		appendMsg(t.renderMsgBase(*open).rows, open.LT)
	}
	t.lineLT = lts
	t.linesDirty = false
	t.linesMemo = out
	t.linesGen = t.client.Gen()
	return out
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
			rows = append(rows, transcriptRow{text: l, ref: ref})
		}
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
	all := t.lines()
	foot := []string{}
	if t.showHelp {
		foot = t.helpLines()
	} else if t.showStatus {
		foot = t.statusPanelLines()
	}
	body := t.h - 2 - len(foot) // bottom rows: panel (if open) + rule + status
	if body < 1 {
		body = 1
	}
	maxOff := len(all) - body
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
	screen := make([]string, t.h)
	for r := 0; r < body; r++ {
		if i := t.offset + r; i < len(all) {
			screen[r] = all[i]
			if t.pattern != nil {
				// Highlight only what's on screen: painting the whole window
				// per repaint is where big transcripts burned CPU.
				screen[r] = t.pattern.highlight(screen[r])
			}
		}
	}
	if t.vmode == visualChar || t.vmode == visualLine || t.vmode == visualColumn {
		aRow, aOK := t.pointToRow(t.vAnchor)
		cRow, cCol, _ := t.resolveCursor()
		aCol := t.vAnchor.col
		if !aOK {
			aCol = 0
		}
		va := visualPos{row: aRow, col: aCol}
		vc := visualPos{row: cRow, col: cCol}
		// Rows outside the viewport (bar the endpoint rows, whose columns
		// shape char/column spans) don't need ANSI-stripping — their spans
		// are discarded below anyway.
		vis := func(r int) string {
			if r != aRow && r != cRow && (r < t.offset || r >= t.offset+body) {
				return ""
			}
			return visibleRowText(all, r)
		}
		spans := visualSpans(t.vmode, va, vc, vis)
		for _, sp := range spans {
			r := sp.row - t.offset
			if r < 0 || r >= body {
				continue
			}
			if sp.to == sp.from && visibleRowText(all, sp.row) == "" {
				// an empty selected line still reads as selected
				screen[r] = screen[r] + visualBgOn + " " + visualBgOff
				continue
			}
			screen[r] = overlayVisual(screen[r], sp.from, sp.to)
		}
	}
	if t.vmode != visualOff {
		cRow, cCol, ok := t.resolveCursor()
		if r := cRow - t.offset; ok && r >= 0 && r < body {
			// Current search match wears the selection background (distinct
			// from other matches' reverse video), then the cursor cell on
			// top — restoring the bg after the cell so the rest of the span
			// keeps it.
			inBg := t.vmode.selecting() // selections already painted this row's bg
			if t.pattern != nil {
				v := visibleRowText(all, cRow)
				for _, loc := range t.pattern.re.FindAllStringIndex(v, -1) {
					if cCol >= loc[0] && cCol < loc[1] {
						screen[r] = overlayVisual(screen[r], loc[0], loc[1])
						inBg = true
						break
					}
				}
			}
			restore := ""
			if inBg {
				restore = visualBgOn
			}
			screen[r] = overlayCursorCell(screen[r], cCol, restore)
		}
	}
	for k, l := range foot {
		if r := body + k; r < t.h-2 {
			screen[r] = l
		}
	}
	rule, status := t.footerRows(len(all), body)
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
	if cur, n, ok := t.matchStats(); ok {
		ind := fmt.Sprintf("%d/%d", cur, n)
		if pos != "" {
			pos = ind + " · " + pos
		} else {
			pos = ind
		}
	}
	rule = "\x1b[2m" + t.status.ruleLine(t.w, pos) + "\x1b[0m"
	if t.inSearch {
		prompt := "/"
		if t.promptFilter {
			prompt = "&"
		}
		line := prompt + t.query
		if t.searchErr != "" {
			line += "  ⟨" + t.searchErr + "⟩"
		}
		return rule, "\x1b[2m" + clipToWidth(line, t.w) + "\x1b[0m"
	}
	line := t.status.statusLine(t.w, true)
	if t.filter != nil {
		// The filter hides content — say so persistently, ahead of the status.
		line = clipToWidth("& "+t.filter.src+" · "+line, t.w)
	}
	return rule, "\x1b[2m" + line + "\x1b[0m"
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

// helpLines is the '?' panel: the footer grown upward into a key reference,
// drawn above the footer while output keeps streaming past above it. Any key
// wipes it. (Deliberately a bottom panel, not a floating overlay: the terminal
// has exactly one alternate buffer, and compositing a float into every live
// repaint buys nothing over this.)
func (t *transcript) helpLines() []string {
	rows := []string{
		"",
		"  j/k · u/d · gg/G    scroll · half-page · top/bottom",
		"  /  ?                regex search forward / backward (smartcase)",
		"  n / N               next / previous match (follows search direction)",
		"  &                   filter: only matching lines ('&' + Enter clears)",
		"  i                   cursor mode — visible cursor + match count",
		"  v / V / ^V          visual select: char / line / column",
		"  h l w e b 0 ^ $     motions (cursor + visual modes)",
		"  y or Enter          (visual) copy selection, exit visual",
		"  ^N/^P + Enter       select whole nodes / expand tools",
		"  ^O                  toggle verbose tool output",
		"  ^L                  listen — stay open after the turn ends",
		"  q                   leave cursor/visual mode (inert otherwise)",
		"  Esc                 step out: selection → cursor → off; then :noh",
		"  ^D                  detach; the turn keeps running",
		"  ^C                  interrupt the turn / close",
		"  ^/                  this help",
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

func (t *transcript) paint(screen []string) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(t.prev) {
			old = t.prev[r]
		}
		if screen[r] != old {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, screen[r])
		}
	}
	b.WriteString("\x1b[?2026l")
	io.WriteString(t.out, b.String())
	t.prev = screen
}

// key handles one navigation/search input byte. Transcript is a locked mode:
// keys only scroll or search — it NEVER self-exits. Exit is Ctrl-D / Ctrl-C,
// handled at the input loop. q and Ctrl-T are deliberately inert here; Esc
// wipes an open panel, else clears search highlight + filter (:noh).
func (t *transcript) key(b byte) {
	t.markDirty()
	if t.inSearch {
		t.searchKey(b)
		t.render()
		return
	}
	if t.vmode != visualOff {
		if t.visualKey(b) {
			return
		}
	}
	if t.showHelp || t.showStatus { // any key wipes the panel; nav keys also still act below
		reopen := byte(0)
		if t.showHelp && b == '!' {
			reopen = '!' // switch panels directly
		}
		if t.showStatus && b == 0x1f {
			reopen = '?'
		}
		t.showHelp, t.showStatus = false, false
		switch {
		case reopen == '!':
			t.showStatus = true
		case reopen == '?':
			t.showHelp = true
		case b == 0x1f || b == '!' || b == 0x1b || b == 'q':
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
		t.inSearch, t.promptFilter, t.promptBack, t.query, t.searchErr = true, false, false, "", ""
	case '?': // vim: backward search
		t.inSearch, t.promptFilter, t.promptBack, t.query, t.searchErr = true, false, true, "", ""
	case '&':
		t.inSearch, t.promptFilter, t.promptBack, t.query, t.searchErr = true, true, false, "", ""
	case 'n':
		t.findNext(t.searchDir())
	case 'N':
		t.findNext(-t.searchDir())
	case 0x1b: // Esc — :noh: drop the highlight and the filter
		t.pattern, t.filter = nil, nil
	case 0x1f: // Ctrl-/ (Ctrl-_): help panel
		t.showHelp = true
	case '!':
		t.showStatus = true
	case 'i':
		t.clearSelection() // node selection yields to the modal cursor
		t.startVisual(visualCursor)
	case 'v':
		t.startVisual(visualChar)
	case 'V':
		t.startVisual(visualLine)
	case 0x16: // Ctrl-V
		t.startVisual(visualColumn)
	case 0x0e: // Ctrl-N
		t.selectNode(1, false)
	case 0x10: // Ctrl-P
		t.selectNode(-1, false)
	case 0x0d, 0x0a:
		t.toggleSelectedTools()
	}
	t.pendG = b == 'g' && !t.pendG
	t.render()
}

func (t *transcript) searchKey(b byte) {
	switch b {
	case 0x0d, 0x0a: // Enter → commit: '/' jumps to the first match, '&' filters
		q := t.query
		if q == "" {
			t.inSearch = false
			if t.promptFilter {
				t.filter = nil // '&' with an empty pattern clears the filter
			}
			return
		}
		p, err := compileSearch(q)
		if err != nil {
			t.searchErr = err.Error() // stay in the prompt; fix or Esc out
			return
		}
		t.inSearch, t.searchErr = false, ""
		if t.promptFilter {
			t.filter = p
			t.resetToTail()
			t.follow = true
			return
		}
		t.pattern = p
		t.searchBack = t.promptBack
		t.find(p, t.searchDir())
		if t.vmode == visualOff && t.hasCursor {
			t.vmode = visualCursor // a landed search leaves you AT the match
		}
	case 0x1b: // Esc → cancel the prompt (keeps any active highlight/filter)
		t.inSearch, t.query, t.searchErr = false, "", ""
	case 0x7f, 0x08: // backspace
		t.searchErr = ""
		if len(t.query) > 0 {
			t.query = t.query[:len(t.query)-1]
		}
	default:
		if b >= 0x20 && b < 0x7f {
			t.searchErr = ""
			t.query += string(b)
		}
	}
}

// find scrolls to the first line at/after the cursor matching p (wrapping),
// falling through to the paged historical search when the window has no match.
func (t *transcript) find(p *searchPattern, dir int) {
	t.pattern = p // the active pattern: highlight-all, n/N, paged continuation
	all := t.lines()
	n := len(all)
	if n == 0 {
		return
	}
	from, fromCol, useCol := t.searchOrigin(all)
	if useCol {
		v := visibleRowText(all, from)
		if dir > 0 {
			if c, ok := p.matchIndexAfter(v, inclusiveEnd(v, fromCol)); ok {
				t.landAt(from, c)
				return
			}
		} else if c, ok := p.lastMatchIndexBefore(v, fromCol); ok {
			t.landAt(from, c)
			return
		}
	}
	for i := 0; i < n; i++ {
		idx := ((from+dir*(1+i))%n + n) % n
		if p.match(all[idx]) {
			t.landRow(idx, all[idx], dir)
			return
		}
	}
	pdir := pageOlder
	if dir > 0 && t.hasNewerHistory() {
		pdir = pageNewer
	}
	t.startPagedSearch(p, pdir, false, dir < 0)
}

// findNext is n/N: jump to the next (dir=1) or previous (dir=-1) match. Past
// the window edge it continues into paged history in that direction; with no
// more history it wraps within the window, vim-style.
func (t *transcript) findNext(dir int) {
	if t.pattern == nil {
		return
	}
	all := t.lines()
	n := len(all)
	if n == 0 {
		return
	}
	// vim semantics: the search starts at the CURSOR — and within the
	// cursor's row it is column-aware, so several matches on one line are
	// visited in order. Outside visual mode the cursor is invisible, so once
	// it scrolls off screen n/N restart from the viewport instead of
	// teleporting back to a stale position.
	startRow, startCol, useCol := t.searchOrigin(all)
	if useCol {
		v := visibleRowText(all, startRow)
		if dir > 0 {
			if c, ok := t.pattern.matchIndexAfter(v, inclusiveEnd(v, startCol)); ok {
				t.landAt(startRow, c)
				return
			}
		} else if c, ok := t.pattern.lastMatchIndexBefore(v, startCol); ok {
			t.landAt(startRow, c)
			return
		}
	}
	for i := startRow + dir; i >= 0 && i < n; i += dir {
		if t.pattern.match(all[i]) {
			t.landRow(i, all[i], dir)
			return
		}
	}
	oldest, haveOldest := t.oldestLT()
	switch {
	case dir > 0 && t.hasNewerHistory():
		t.startPagedSearch(t.pattern, pageNewer, true, false)
	case dir < 0 && !t.noMoreOlder && haveOldest && oldest > 1:
		t.startPagedSearch(t.pattern, pageOlder, true, true)
	default:
		t.windowWrapScan(dir < 0)
	}
}

// landRow lands on a row-level match: forward searches take the row's first
// match, backward its last (vim). landAt then positions cursor + viewport.
func (t *transcript) landRow(row int, line string, dir int) {
	v := line
	if strings.ContainsRune(v, '\x1b') {
		v, _ = visibleWithMap(v)
	}
	col := 0
	if t.pattern != nil {
		if dir < 0 {
			if c, ok := t.pattern.lastMatchIndexBefore(v, len(v)+1); ok {
				col = c
			}
		} else if c, ok := t.pattern.matchIndex(line); ok {
			col = c
		}
	}
	t.landAt(row, col)
}

// landAt parks the cursor (LT-anchored) on a search landing. Outside visual
// mode the match row jumps to the viewport top (classic pager). In visual
// mode the viewport scrolls only as far as needed — jumping would push the
// selection's other end off screen.
func (t *transcript) landAt(row, col int) {
	t.vCursor = t.rowToPoint(row, col)
	t.hasCursor = true
	if t.vmode == visualOff {
		t.offset = row
	} else {
		t.ensureCursorVisible(0)
	}
	t.stopFollowing()
}

// matchStats reports the cursor's match ordinal and the total matches across
// the rendered window ("3/17"), for the footer. Counted over the retained
// window — the honest scope, since older history isn't loaded. Memoized on
// (pattern, window size, cursor) so streaming repaints don't re-scan.
func (t *transcript) matchStats() (cur, total int, ok bool) {
	if t.pattern == nil || !t.hasCursor || t.vmode == visualOff {
		return 0, 0, false
	}
	all := t.lines()
	cRow, cCol, _ := t.resolveCursor()
	key := matchStatsKey{src: t.pattern.src, rows: len(all)}
	if t.statsCache.key != key {
		// The expensive full-window scan runs only when the pattern or the
		// window itself changes — cursor motion just re-ranks the cached
		// positions.
		var positions []matchPos
		for i := 0; i < len(all); i++ {
			v := visibleRowText(all, i)
			for _, loc := range t.pattern.re.FindAllStringIndex(v, -1) {
				positions = append(positions, matchPos{row: i, col: loc[0]})
			}
		}
		t.statsCache = matchStatsCache{key: key, positions: positions}
	}
	total = len(t.statsCache.positions)
	for _, mp := range t.statsCache.positions {
		if mp.row < cRow || (mp.row == cRow && mp.col <= cCol) {
			cur++
		}
	}
	if cur == 0 && total > 0 {
		cur = 1
	}
	return cur, total, total > 0
}

// searchDir is the committed search's direction: 'n' continues it, 'N'
// reverses — vim's rule, so a '?' search makes n walk backward.
func (t *transcript) searchDir() int {
	if t.searchBack {
		return -1
	}
	return 1
}

// searchOrigin picks where a search continues from: the resolved cursor with
// column awareness when it's trustworthy (visual mode, or visible in the
// viewport), else the viewport top without a column.
func (t *transcript) searchOrigin(all []string) (row, col int, useCol bool) {
	if !t.hasCursor {
		return t.offset, 0, false
	}
	r, c, ok := t.resolveCursor()
	if !ok {
		return t.offset, 0, false
	}
	if t.vmode == visualOff {
		body := t.bodyRows()
		if r < t.offset || r >= t.offset+body {
			return t.offset, 0, false
		}
	}
	if r >= len(all) {
		return t.offset, 0, false
	}
	return r, c, true
}

// rowToPoint anchors a row index to (LT, within, col) against lineLT.
func (t *transcript) rowToPoint(row, col int) visualPoint {
	if row < 0 || row >= len(t.lineLT) {
		return visualPoint{col: col}
	}
	lt := t.lineLT[row]
	s := row
	for s > 0 && t.lineLT[s-1] == lt {
		s--
	}
	return visualPoint{lt: lt, within: row - s, col: col}
}

// pointToRow resolves an anchored endpoint to the current lines() index,
// clamped into the message's present extent. ok is false when the anchored
// message is not in the window (paged out, filtered, folded away) — the
// fallback row is the viewport top, and callers must NOT reuse the stale
// column against it.
func (t *transcript) pointToRow(pt visualPoint) (int, bool) {
	first, last := -1, -1
	for i, lt := range t.lineLT {
		if lt == pt.lt {
			if first < 0 {
				first = i
			}
			last = i
		} else if first >= 0 {
			break
		}
	}
	if first < 0 {
		if n := len(t.lineLT); n > 0 {
			if t.offset < n {
				return t.offset, false
			}
			return n - 1, false
		}
		return 0, false
	}
	r := first + pt.within
	if r > last {
		r = last
	}
	return r, true
}

// resolveCursor resolves the visual cursor to (row, col), zeroing the column
// when its anchored message left the window.
func (t *transcript) resolveCursor() (int, int, bool) {
	row, ok := t.pointToRow(t.vCursor)
	col := t.vCursor.col
	if !ok {
		col = 0
	}
	return row, col, ok
}

// bodyRows is the content height render uses: the two footer rows plus any
// open panel come off the top of t.h. ensureCursorVisible must agree with
// render or the cursor can hide behind the panel.
func (t *transcript) bodyRows() int {
	body := t.h - 2
	if t.showHelp {
		body -= len(t.helpLines())
	} else if t.showStatus {
		body -= len(t.statusPanelLines())
	}
	if body < 1 {
		body = 1
	}
	return body
}

// visibleRowText returns the visible (ANSI-stripped) text of a rendered row.
func visibleRowText(all []string, r int) string {
	if r < 0 || r >= len(all) {
		return ""
	}
	v := all[r]
	if strings.ContainsRune(v, '\x1b') {
		v, _ = visibleWithMap(v)
	}
	return v
}

// visualKey handles one byte while a visual mode is active. Returns true when
// the byte was consumed. y/Enter are handled by the input loop (clipboard).
func (t *transcript) visualKey(b byte) bool {
	all := t.lines()
	n := len(all)
	if n == 0 {
		return false
	}
	cur, col, _ := t.resolveCursor()
	clampRow := func(r int) int {
		if r < 0 {
			return 0
		}
		if r >= n {
			return n - 1
		}
		return r
	}
	set := func(row, c int) {
		t.vCursor = t.rowToPoint(clampRow(row), c)
	}
	switch b {
	case 0x1b: // Esc: selection → cursor mode (vim); cursor mode → out
		if t.vmode == visualCursor {
			t.vmode = visualOff
		} else {
			t.vmode = visualCursor
		}
	case 'q': // q: all the way out (the base pager keeps q inert)
		t.vmode = visualOff
	case 'i':
		t.vmode = visualCursor
	case 'v':
		if t.vmode == visualChar {
			t.vmode = visualCursor
		} else {
			t.vAnchor = t.vCursor // selection starts at the cursor
			t.vmode = visualChar
		}
	case 'V':
		if t.vmode == visualLine {
			t.vmode = visualCursor
		} else {
			t.vAnchor = t.vCursor
			t.vmode = visualLine
		}
	case 0x16: // Ctrl-V
		if t.vmode == visualColumn {
			t.vmode = visualCursor
		} else {
			t.vAnchor = t.vCursor
			t.vmode = visualColumn
		}
	case 'j', 'k':
		dir := 1
		if b == 'k' {
			dir = -1
		}
		next := clampRow(cur + dir)
		set(next, clampCol(visibleRowText(all, next), col))
		if dir < 0 {
			t.checkOlder = true
		} else {
			t.checkNewer = true
		}
	case 'h':
		set(cur, moveCol(visibleRowText(all, cur), col, -1))
	case 'l':
		set(cur, moveCol(visibleRowText(all, cur), col, +1))
	case 'w': // next word start; at end of line, hop to the next row
		v := visibleRowText(all, cur)
		if nc := wordForward(v, col); nc != col {
			set(cur, nc)
		} else if cur+1 < n {
			set(cur+1, firstNonBlank(visibleRowText(all, cur+1)))
			t.checkNewer = true
		}
	case 'e':
		v := visibleRowText(all, cur)
		if nc := wordEnd(v, col); nc != col {
			set(cur, nc)
		} else if cur+1 < n {
			nv := visibleRowText(all, cur+1)
			set(cur+1, wordEnd(nv, firstNonBlank(nv)))
			t.checkNewer = true
		}
	case 'b': // previous word start; at line start, hop to the previous row end
		v := visibleRowText(all, cur)
		if nc := wordBack(v, col); nc != col {
			set(cur, nc)
		} else if cur > 0 {
			set(cur-1, lastRuneStart(visibleRowText(all, cur-1)))
			t.checkOlder = true
		}
	case '0':
		set(cur, 0)
	case '^':
		set(cur, firstNonBlank(visibleRowText(all, cur)))
	case '$':
		set(cur, lastRuneStart(visibleRowText(all, cur)))
	case 'G':
		set(n-1, 0)
	case 'g':
		if t.pendG {
			set(0, 0)
			t.checkOlder = true
		}
		t.pendG = !t.pendG
		t.ensureCursorVisible(n)
		t.render()
		return true
	case 'n':
		t.findNext(t.searchDir())
	case 'N':
		t.findNext(-t.searchDir())
	default:
		return false // scroll/search/panel keys keep their normal meaning
	}
	t.pendG = false
	t.ensureCursorVisible(n)
	t.render()
	return true
}

// ensureCursorVisible scrolls the viewport just enough to keep the visual
// cursor's row on screen.
func (t *transcript) ensureCursorVisible(total int) {
	body := t.bodyRows()
	row, _, _ := t.resolveCursor()
	if row < t.offset {
		t.offset = row
		t.stopFollowing()
	} else if row >= t.offset+body {
		t.offset = row - body + 1
		t.stopFollowing()
	}
	_ = total
}

// startVisual enters a visual mode, anchoring at the current cursor (or the
// viewport top when search never placed one).
func (t *transcript) startVisual(mode visualMode) {
	t.lines() // lineLT must reflect the current rows before anchoring
	if !t.hasCursor {
		t.vCursor = t.rowToPoint(t.offset, 0)
		t.hasCursor = true
	}
	t.vAnchor = t.vCursor
	t.vmode = mode
}

// visualYankText extracts the current selection's visible text and leaves
// visual mode (vim: y exits). ok is false when nothing is selected.
func (t *transcript) visualYankText() (string, bool) {
	if !t.vmode.selecting() {
		return "", false
	}
	all := t.lines()
	aRow, aOK := t.pointToRow(t.vAnchor)
	cRow, cCol, cOK := t.resolveCursor()
	if !aOK || !cOK {
		// An endpoint's message left the window: yanking would copy text the
		// user never selected. Drop the selection instead.
		t.vmode = visualOff
		t.render()
		return "", false
	}
	a := visualPos{row: aRow, col: t.vAnchor.col}
	c := visualPos{row: cRow, col: cCol}
	spans := visualSpans(t.vmode, a, c, func(r int) string { return visibleRowText(all, r) })
	t.vmode = visualOff
	t.render()
	return visualYank(spans, func(r int) string { return visibleRowText(all, r) }), true
}

// windowWrapScan is the n/N wrap: with the paged direction exhausted (or no
// history there), rescan the whole window from the far end — vim's "search
// hit BOTTOM, continuing at TOP", scoped to the retained window rather than
// re-walking the entire aria on every keypress.
func (t *transcript) windowWrapScan(backward bool) {
	if t.pattern == nil {
		return
	}
	all := t.lines()
	n := len(all)
	if n == 0 {
		return
	}
	dir, start := 1, 0
	if backward {
		dir, start = -1, n-1
	}
	for i := start; i >= 0 && i < n; i += dir {
		if t.pattern.match(all[i]) {
			t.landRow(i, all[i], dir) // may wrap to the cursor's own row (vim)
			return
		}
	}
}

// startPagedSearch arms the exhaustive historical search in one direction,
// snapshotting the window so an unmatched search restores it (finishSearch).
func (t *transcript) startPagedSearch(p *searchPattern, dir transcriptPageDirection, wrapWindow, backward bool) {
	t.search = &transcriptSearch{
		query: p.src, pages: append([]transcriptPage(nil), t.pages...),
		newer: append([]pageDesc(nil), t.newer...), offset: t.offset,
		follow: t.follow, noMoreOlder: t.noMoreOlder,
		direction: dir, wrapWindow: wrapWindow, backward: backward,
	}
	t.stopFollowing()
	if dir == pageNewer {
		t.checkNewer = true
	} else {
		t.checkOlder = true
	}
}

func (t *transcript) findPage(p *searchPattern, messages []aria.Message, backward bool) bool {
	if p == nil {
		return false
	}
	// Only an explicit backward search (N) lands on the LAST match of the
	// page; a '/' falling through into older history keeps its historical
	// first-match landing.
	order := messages
	if backward {
		order = make([]aria.Message, len(messages))
		copy(order, messages)
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	for _, m := range order {
		if !t.messageMayRenderQuery(m, p) {
			continue
		}
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[m.LT] = rows
		}
		hit := false
		for _, row := range rows.rows {
			if p.match(row.text) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		all := t.lines()
		first, last := -1, -1
		for i, line := range all {
			if t.lineLT[i] == m.LT && p.match(line) {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 {
			continue // an active '&' filter can hide this message's match — keep scanning the page
		}
		if backward {
			t.landRow(last, all[last], -1)
		} else {
			t.landRow(first, all[first], 1)
		}
		return true
	}
	return false
}

func (t *transcript) messageMayRenderQuery(m aria.Message, p *searchPattern) bool {
	if p.lit == "" {
		return true // regex: literal-containment pruning is unsound — render and scan
	}
	q := p.lit
	contains := func(hay string) bool { return p.probe(hay) }
	if contains(messageHeader(m.Role)) {
		return true
	}
	verbose := false
	if view, ok := t.view.(*ariaView); ok && view.settings != nil {
		verbose = view.settings.verbose
	}
	for i, n := range m.Nodes {
		if markdownMayRenderQuery(p.foldForProbe(n.Markdown), q) || contains(n.Name) ||
			contains(n.Summary) || contains(n.Output) {
			return true
		}
		if n.Type == livedoc.NodeSteering && contains("↳ you") {
			return true
		}
		if n.Type != livedoc.NodeTool {
			continue
		}
		if n.Name == "" && contains("tool") {
			return true
		}
		glyph := "✓✗" + string(livedoc.SpinnerFrames)
		if contains(glyph) {
			return true
		}
		if n.StartedAt != 0 {
			if contains(toolDuration(n, time.Now())) {
				return true
			}
			if verbose && (contains("started "+formatToolTime(n.StartedAt)) ||
				contains("finished "+formatToolTime(n.FinishedAt))) {
				return true
			}
		}
		if verbose {
			for k, v := range n.Args {
				if contains(fmt.Sprintf("%s=%v", k, v)) {
					return true
				}
			}
		}
		if !t.expanded[nodeRef{lt: m.LT, index: i}] && n.Output != "" {
			total := 1 + strings.Count(strings.TrimRight(n.Output, "\n"), "\n")
			if total > nodeBashCapDefault &&
				contains(fmt.Sprintf("last %d of %d lines", nodeBashCapDefault, total)) {
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
	if origin.wrapWindow {
		t.windowWrapScan(origin.backward) // n/N: exhausted direction → wrap
	}
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
