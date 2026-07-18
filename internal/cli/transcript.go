package cli

import (
	"fmt"
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
	out       io.Writer
	view      ldrender.NodeView
	client    *aria.Client
	figaroID  string    // shown in the footer (blank → omitted)
	startedAt time.Time // session start; the footer time, mirroring the incipit bookend
	status    *sessionStatus

	active   bool
	showHelp bool // '?': the footer grows into a key-reference panel
	w, h     int
	tick     int

	prev   []string // last painted screen (full-frame diff)
	lineLT []int    // LT owning each line of lines(), for resize anchoring
	offset int      // top line of the viewport into lines()
	follow bool     // stick to the bottom on new content
	pendG  bool     // saw one 'g' (for gg)

	inSearch bool
	query    string

	// Lazy history paging: the pager opens on the recent window and pulls older
	// messages via keyset ReadBefore only when you scroll near the top ("like
	// Twitter"). checkOlder is armed by an upward scroll; noMoreOlder latches
	// once a fetch comes back empty.
	checkOlder  bool
	checkNewer  bool
	noMoreOlder bool
	pages       []transcriptPage
	newer       []int
	search      *transcriptSearch
	heldOpen    *aria.Message

	// rowCache memoizes unstyled rows of committed messages. Selection is
	// applied after retrieval, so moving through nodes never re-renders prose.
	rowCache  map[int]cachedMessage
	cacheW    int
	nodeRows  map[nodeRef]nodeSpan
	selection nodeSelection
	expanded  map[nodeRef]bool
}

type transcriptPage struct {
	before   int
	messages []aria.Message
}

type transcriptSearch struct {
	query       string
	pages       []transcriptPage
	newer       []int
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
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	return &transcript{
		out: out, view: view, client: client, figaroID: figaroID, startedAt: startedAt,
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
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query = false, false, ""
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
	transcriptPageSize  = 30
	transcriptPageLimit = 3
	transcriptTailLimit = 2 * transcriptPageSize
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
		return transcriptPageRequest{before: t.newer[len(t.newer)-1], direction: pageNewer}, true
	}
	return transcriptPageRequest{}, false
}

func (t *transcript) applyPage(req transcriptPageRequest, r aria.AriaRead) {
	if !t.active {
		return
	}
	messages := committedMessages(r.Committed)
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
	searching := t.search != nil
	anchorLT, within := 0, 0
	if !searching {
		anchorLT, within = t.viewportAnchor()
	}
	page := transcriptPage{before: req.before, messages: messages}
	switch req.direction {
	case pageOlder:
		t.pages = append([]transcriptPage{page}, t.pages...)
	case pageNewer:
		t.pages = append(t.pages, page)
		if len(t.newer) > 0 {
			t.newer = t.newer[:len(t.newer)-1]
		}
	}
	t.trimPages(req.direction)
	if searching {
		if t.findPage(t.search.query, messages) {
			t.search = nil
		} else if t.search.direction == pageNewer {
			if len(t.newer) > 0 {
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
		if t.selection.active && t.pageContainsSelection(t.pages[drop]) {
			return
		}
		page := t.pages[drop]
		if direction == pageOlder {
			t.newer = append(t.newer, page.before)
		}
		t.dropPage(page)
		t.pages = append(t.pages[:drop], t.pages[drop+1:]...)
		if direction == pageNewer {
			t.noMoreOlder = false
		}
	}
}

func (t *transcript) pageContainsSelection(page transcriptPage) bool {
	if len(page.messages) == 0 {
		return false
	}
	first, last := t.selection.anchor.lt, t.selection.focus.lt
	if first > last {
		first, last = last, first
	}
	pageFirst := page.messages[0].LT
	pageLast := page.messages[len(page.messages)-1].LT
	return pageFirst <= last && pageLast >= first
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

func (t *transcript) resetToTail() {
	v := t.client.View()
	closed := v.Closed
	if len(closed) > transcriptPageSize {
		closed = closed[len(closed)-transcriptPageSize:]
	}
	t.pages = nil
	if len(closed) > 0 {
		t.pages = []transcriptPage{{before: closed[len(closed)-1].LT + 1, messages: closed}}
	}
	t.newer = nil
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

func (t *transcript) olderCursor() (int, bool) {
	req, ok := t.pageCursor()
	return req.before, ok && req.direction == pageOlder
}

func (t *transcript) applyOlder(r aria.AriaRead) {
	messages := t.messages()
	if len(messages) == 0 {
		t.noMoreOlder = true
		return
	}
	t.applyPage(transcriptPageRequest{before: messages[0].LT, direction: pageOlder}, r)
}

func (t *transcript) resize(w, h int) {
	// Anchor on the message at the viewport top: a width change re-wraps rows and
	// changes line counts, so keeping the raw line offset would jump the view.
	// Record the top message's LT + how many lines into it we are, then restore
	// after re-rendering at the new width. (Skipped when following the tail.)
	anchorLT, within := t.viewportAnchor()
	t.w, t.h = w, h
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

func (t *transcript) invalidateRows() {
	t.rowCache = map[int]cachedMessage{}
}

// lines renders the retained message window and live tail to physical rows.
// Committed messages are immutable, so their rendered rows are cached by LT;
// only the open message renders every frame.
func (t *transcript) lines() []string {
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
	}
	body := t.h - 1 - len(foot) // bottom rows: help panel (if open) + footer
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
		}
	}
	for k, l := range foot {
		if r := body + k; r < t.h-1 {
			screen[r] = l
		}
	}
	screen[t.h-1] = t.footer(len(all), body)
	t.paint(screen)
}

// footer is the transcript's single status line, in the same rule grammar as
// the incipit bookend ("─── id · time ───…") so both modes speak one visual
// language: left tokens are the aria id, mantra, context consumption, token
// cost, session start time, and the "? help" hook; the scroll position sits
// right-aligned inside the trailing rule. Narrow panes retain the id and mantra
// before shedding secondary status details.
func (t *transcript) footer(total, body int) string {
	if t.inSearch {
		return "\x1b[2m" + clipToWidth("/"+t.query, t.w) + "\x1b[0m"
	}
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
	right := ""
	if pos != "" {
		right = " " + pos + " ───"
	}
	return "\x1b[2m" + sessionStatusRule(t.status, t.w, right) + "\x1b[0m"
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
		"  /                   search (Enter jump · Esc cancel)",
		"  y                   copy aria id",
		"  ^O                  toggle verbose tool output",
		"  ^N/^P               select next/previous node",
		"  ^N/^P + Shift       extend node selection (Alt+^N/^P fallback)",
		"  Enter / ^C          expand tools / copy selected node(s)",
		"  ^L                  listen — stay open after the turn ends",
		"  ^D                  detach; the turn keeps running",
		"  ^C                  interrupt the turn / close",
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
// handled at the input loop. q, Esc, and Ctrl-T are deliberately inert here.
func (t *transcript) key(b byte) {
	if t.inSearch {
		t.searchKey(b)
		t.render()
		return
	}
	if t.showHelp { // any key wipes the panel; nav keys also still act below
		t.showHelp = false
		if b == '?' || b == 0x1b || b == 'q' {
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
	case '?':
		t.showHelp = true
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
	case 0x0d, 0x0a: // Enter → jump to first match
		t.inSearch = false
		t.find(t.query)
	case 0x1b: // Esc → cancel
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
	all := t.lines()
	if len(all) == 0 {
		return
	}
	for i := 0; i < len(all); i++ {
		idx := (t.offset + 1 + i) % len(all)
		if searchContains(all[idx], q) {
			t.offset = idx
			t.stopFollowing()
			return
		}
	}
	t.search = &transcriptSearch{
		query: q, pages: append([]transcriptPage(nil), t.pages...),
		newer: append([]int(nil), t.newer...), offset: t.offset,
		follow: t.follow, noMoreOlder: t.noMoreOlder,
		direction: pageOlder,
	}
	t.stopFollowing()
	if len(t.newer) > 0 {
		t.search.direction = pageNewer
		t.checkNewer = true
	} else {
		t.checkOlder = true
	}
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
			if searchContains(row.text, q) {
				all := t.lines()
				for i, line := range all {
					if t.lineLT[i] == m.LT && searchContains(line, q) {
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

func searchContains(row, q string) bool {
	if !strings.ContainsRune(row, '\x1b') {
		return strings.Contains(row, q)
	}
	var visible strings.Builder
	visible.Grow(len(row))
	for i := 0; i < len(row); {
		if row[i] != '\x1b' {
			visible.WriteByte(row[i])
			i++
			continue
		}
		if i+1 >= len(row) {
			break
		}
		if row[i+1] == '[' {
			i += 2
			for i < len(row) {
				final := row[i]
				i++
				if final >= 0x40 && final <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	return strings.Contains(visible.String(), q)
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
	t.newer = append([]int(nil), origin.newer...)
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
