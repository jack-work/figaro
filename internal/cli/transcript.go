package cli

import (
	"fmt"
	"io"
	"strings"

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
// Keys: j/k line, u/d half-page, gg/G top/bottom, / literal search, q/Esc/Ctrl-T
// exit. Not safe for concurrent use; the caller serializes all entry points.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client

	active bool
	w, h   int
	tick   int

	prev   []string // last painted screen (full-frame diff)
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

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client) *transcript {
	return &transcript{out: out, view: view, client: client, w: w, h: h, rowCache: map[int][]string{}}
}

// enter switches to the alternate screen and draws the transcript at the bottom.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query = false, false, ""
	io.WriteString(t.out, altScreenOn+ldmouse.Enable+cursorHide+"\x1b[2J")
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
	t.w, t.h = w, h
	t.prev = nil
	io.WriteString(t.out, "\x1b[2J")
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
	rule := func() { out = append(out, "", dimTransRule(t.w), "") }
	for _, m := range v.Closed {
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsg(m)
			t.rowCache[m.LT] = rows
		}
		out = append(out, rows...)
		rule()
	}
	if v.Open != nil {
		out = append(out, t.renderMsg(*v.Open)...)
		rule()
	}
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
	body := t.h - 1 // bottom row is the status bar
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
	screen[t.h-1] = t.status(len(all))
	t.paint(screen)
}

func (t *transcript) status(total int) string {
	if t.inSearch {
		return "\x1b[7m" + clipToWidth("/"+t.query, t.w) + "\x1b[0m"
	}
	pos := "TOP"
	if total > t.h-1 {
		end := t.offset + t.h - 1
		if end > total {
			end = total
		}
		pos = fmt.Sprintf("%d-%d/%d", t.offset+1, end, total)
		if t.follow {
			pos += " (live)"
		}
	}
	s := fmt.Sprintf(" transcript  %s   j/k u/d  gg/G  / search  q exit ", pos)
	return "\x1b[7m" + clipToWidth(s, t.w) + "\x1b[0m"
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

// key handles one input byte; returns true when the transcript should close.
func (t *transcript) key(b byte) (done bool) {
	if t.inSearch {
		t.searchKey(b)
		t.render()
		return false
	}
	switch b {
	case 'q', 0x1b, 0x14: // q / Esc / Ctrl-T
		return true
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
	}
	t.pendG = b == 'g' && !t.pendG
	t.render()
	return false
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
