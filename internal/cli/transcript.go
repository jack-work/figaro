package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
)

const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
)

// transcript is a full-screen, live-updating pager over the whole conversation,
// drawn on the alternate screen and toggled with Ctrl-T. Because it owns a fixed
// canvas with its own scroll buffer (over the in-memory aria model), it is both
// resize-clean and scrollable — the two things inline can't do at once. It reads
// the shared aria.Client, so it streams live: at the bottom it follows new
// output, otherwise it holds your scroll position.
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
	noMoreOlder bool

	// rowCache memoizes the rendered rows of committed (immutable) messages by
	// LT, so the expensive markdown render runs once per message instead of on
	// every frame. Invalidated wholesale when the width changes (cacheW).
	rowCache map[int][]string
	cacheW   int
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client, figaroID string, startedAt time.Time) *transcript {
	return &transcript{
		out: out, view: view, client: client, figaroID: figaroID, startedAt: startedAt,
		status: newSessionStatus(figaroID, startedAt), w: w, h: h, rowCache: map[int][]string{},
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
	t.follow = false
	if delta < 0 {
		t.checkOlder = true // scrolled up: maybe page older history
	}
	t.render()
}

// transcriptPageSize is how many older messages a single scroll-up fetch pulls.
const transcriptPageSize = 30

// olderCursor returns (beforeLT, true) when a recent upward scroll has brought
// the viewport near the top and older history may still exist — the caller
// should fetch ReadBefore(beforeLT, transcriptPageSize) off-lock and hand the
// result to applyOlder. It consumes the armed flag.
func (t *transcript) olderCursor() (int, bool) {
	if !t.checkOlder || t.noMoreOlder {
		return 0, false
	}
	t.checkOlder = false
	if t.offset >= t.h { // not near the top yet
		return 0, false
	}
	v := t.client.View()
	if len(v.Closed) == 0 {
		return 0, false
	}
	oldest := v.Closed[0].LT
	if oldest <= 1 { // LTs start at 1 — nothing older
		t.noMoreOlder = true
		return 0, false
	}
	return oldest, true
}

// applyOlder folds paged-in older history into the model and keeps the viewport
// anchored: older messages sort to the front, so the offset shifts down by the
// number of rows added above. An empty result latches noMoreOlder.
func (t *transcript) applyOlder(r aria.AriaRead) {
	if !t.active {
		return
	}
	if len(r.Committed) == 0 {
		t.noMoreOlder = true
		return
	}
	before := len(t.lines())
	t.client.Apply(r)
	after := len(t.lines())
	if d := after - before; d > 0 {
		t.offset += d
	}
	t.render()
}

func (t *transcript) resize(w, h int) {
	// Anchor on the message at the viewport top: a width change re-wraps rows and
	// changes line counts, so keeping the raw line offset would jump the view.
	// Record the top message's LT + how many lines into it we are, then restore
	// after re-rendering at the new width. (Skipped when following the tail.)
	anchorLT, within := 0, 0
	if !t.follow && t.offset < len(t.lineLT) {
		anchorLT = t.lineLT[t.offset]
		s := t.offset
		for s > 0 && t.lineLT[s-1] == anchorLT {
			s--
		}
		within = t.offset - s
	}
	t.w, t.h = w, h
	t.prev = nil // full repaint (diff vs nil); no \x1b[2J, which flickers
	t.lines()    // re-render at the new width, repopulating lineLT
	if anchorLT != 0 {
		for i, lt := range t.lineLT {
			if lt == anchorLT {
				t.offset = i + within
				break
			}
		}
	}
	t.render()
}

// lines renders the whole conversation (committed messages + the open one) to
// physical rows, separated by a rule. Committed messages are immutable, so their
// rendered rows are cached by LT — only the open message renders every frame.
func (t *transcript) lines() []string {
	if t.cacheW != t.w { // width changed: cached rows are stale
		t.rowCache = map[int][]string{}
		t.cacheW = t.w
	}
	v := t.client.View()
	var out []string
	var lts []int // LT owning each line (0 for separator rules), parallel to out
	appendMsg := func(rows []string, lt int) {
		if len(out) > 0 { // rule separator BETWEEN messages only — the footer
			out = append(out, "", dimTransRule(t.w), "") // seals the last one, so a
			lts = append(lts, lt, lt, lt)                // trailing rule+blank would
		} // double up against it
		for _, r := range rows {
			out = append(out, r)
			lts = append(lts, lt)
		}
	}
	for _, m := range v.Closed {
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsg(m)
			t.rowCache[m.LT] = rows
		}
		appendMsg(rows, m.LT)
	}
	if v.Open != nil {
		appendMsg(t.renderMsg(*v.Open), v.Open.LT)
	}
	t.lineLT = lts
	return out
}

// renderMsg renders one message's nodes to clipped physical rows, optionally
// prefixed with the role header ("❯ you" / "‹ figaro"). The spinner tick only
// affects a running tool, which lives on the open message — committed messages
// render identically every time, so their result is safe to cache.
func (t *transcript) renderMsg(m aria.Message) []string {
	var rows []string
	if h := messageHeader(m.Role); h != "" {
		rows = append(rows, h, "")
	}
	for k, n := range m.Nodes {
		if k > 0 {
			rows = append(rows, "")
		}
		for _, l := range t.view.Render(n, t.w, t.tick) {
			rows = append(rows, clipToWidth(l, t.w))
		}
	}
	return rows
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
		t.follow = false
	case 'k':
		t.offset--
		t.follow = false
		t.checkOlder = true
	case 'd':
		t.offset += t.h / 2
		t.follow = false
	case 'u':
		t.offset -= t.h / 2
		t.follow = false
		t.checkOlder = true
	case 'G':
		t.follow = true
	case 'g':
		if t.pendG {
			t.offset, t.follow = 0, false
			t.checkOlder = true
		}
	case '/':
		t.inSearch, t.query = true, ""
	case '?':
		t.showHelp = true
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
		if strings.Contains(all[idx], q) {
			t.offset, t.follow = idx, false
			return
		}
	}
}

func dimTransRule(w int) string {
	if w < 3 {
		w = 3
	}
	return "\x1b[2m" + strings.Repeat("─", w) + "\x1b[0m"
}
