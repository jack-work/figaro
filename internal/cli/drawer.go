package cli

// THE DRAWER: the pager's one transient region.
//
// The bottom of the pager used to be a scramble. Five panel renderers, each
// with its own clipping rule (t.h-4, t.h/3, t.h/4), none paginated, none
// selectable, and none carrying chrome that said what it was or how to leave
// it. Worse, THE STATUS BAR WAS A CONTENDED RESOURCE: the search box, the
// command line and every error message took that row for themselves, so the
// mantra, the context percentage and the cost -- the only things on screen that
// are always true -- disappeared exactly when something had gone wrong, which
// is when a reader most needs to know which aria they are in and how much room
// is left in it.
//
// One rule now governs the bottom of the screen:
//
//	┌ the transcript's rule ──────────────────── aria 3b7aff0a · 1–29/118 ───
//	│ Queue:                          ← the DRAWER: a title and rows
//	│   item 1
//	│ ▸ item 2                        ← at most one selected row
//	│   item 3
//	│ … 749 more                      ← a page marker, never a silent truncation
//	├ the drawer's closing rule ─────────────────────────────────────────────
//	└ mantra · ctx 55.8% · cost 162.9k · 11:19:45   ← THE STATUS BAR, INVIOLABLE
//
// Everything that is not the transcript and not the status bar is a drawer:
// help, figaro status, the queue, a command's output, completion candidates,
// an error, and the command line itself. One at a time, Esc closes it, and
// nothing else may write to the status row ever again.

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

// drawerPageRows is THE page size, fixed rather than derived from the pane.
// Gluck: "Max page size should be fixed ideally". A drawer whose height moves
// with the terminal makes every position ("… 749 more") a different promise on
// a different screen, and makes ^N stepping past the bottom edge unpredictable.
// A short pane still clips below this; a tall one does not grow past it.
const drawerPageRows = 12

// drawerKind names what a drawer holds, which is the only thing the rest of the
// pager needs to know about it: whether it takes keys (the command line), and
// whether it has rows to select among.
type drawerKind uint8

const (
	drawerNone    drawerKind = iota
	drawerMessage            // one line: an error, a note, a result
	drawerList               // rows, paginated, optionally selectable
	drawerInput              // the command line or the search box
	drawerLive               // a HOSTED verb: it renders its own rows and takes keys
)

// drawerRow is one line of a list drawer, plus what a selection ACTION would
// operate on. text is what is drawn; yank is what `y` copies; id is what a verb
// key (`x` on a queued message) addresses. A row with an empty yank and id is
// chrome -- a header, a blank, a page marker -- and cannot be selected.
type drawerRow struct {
	text string
	yank string
	id   string
}

// staticRow is a row that is drawn but never selected: a header, a blank, a
// page marker. (Not "chromeRow": transcript_mouse_test.go already owns that
// name for a different idea -- a painted row belonging to no node.)
func staticRow(text string) drawerRow { return drawerRow{text: text} }

func (r drawerRow) selectable() bool { return r.yank != "" || r.id != "" }

// drawer is the region itself. The zero value is closed.
type drawer struct {
	kind  drawerKind
	name  string // which drawer this is, for the toggle keys: "help", "queue"…
	title string // drawn as the drawer's first row when non-empty
	rows  []drawerRow

	// cursor is the selected row index into rows, or -1 for no selection. Only
	// selectable rows may hold it; moveSelection skips the chrome.
	cursor int
	// flash is a transient confirmation drawn in the closing rule -- "yanked
	// 655a03cf" -- and cleared by the next key. It lives HERE rather than going
	// through the message drawer because a yank must not destroy the list it
	// was yanking from, which is exactly what the first cut did.
	flash string
	// live is the hosted verb, when this is a drawerLive. The drawer asks it
	// for rows and forwards keys to it; it knows nothing about drawers and the
	// drawer knows nothing about forms. See cmdkit.LiveView.
	live cmdkit.LiveView
	// top is the first visible row: pagination is a window over rows, not a
	// truncation of them, so "… N more" can be honest in both directions.
	top int
}

func (d *drawer) open() bool { return d.kind != drawerNone }

// close empties the drawer. Esc reaches here from every kind.
func (d *drawer) close() {
	if d.live != nil {
		d.live.Close()
	}
	*d = drawer{}
}

// showLive hosts a verb that renders itself. The drawer owns the region and
// the dismissal; the view owns everything inside it.
func (d *drawer) showLive(name string, v cmdkit.LiveView) {
	*d = drawer{kind: drawerLive, name: name, live: v, cursor: -1}
}

// showMessage is the one-line drawer: an error, a result, a note. It carries
// its own dismissal hint because a message that cannot be dismissed is a
// message that has taken the screen hostage.
func (d *drawer) showMessage(text string) {
	*d = drawer{kind: drawerMessage, name: "message", cursor: -1,
		rows: []drawerRow{staticRow(text)}}
}

// showList opens a list drawer, selecting the first selectable row when the
// caller asked for selection.
func (d *drawer) showList(name, title string, rows []drawerRow, selectable bool) {
	*d = drawer{kind: drawerList, name: name, title: title, rows: rows, cursor: -1}
	if selectable {
		d.cursor = d.nextSelectable(-1, 1)
		d.scrollToCursor()
	}
}

// visible is how many rows of the list fit: the fixed page, bounded by what the
// pane can actually give. h is the whole pane.
func (d *drawer) visible(h int) int {
	// The stanza costs two rules and the status row; leave at least three rows
	// of transcript so the drawer never becomes the whole screen.
	room := h - 3 - 3
	if d.title != "" {
		room--
	}
	n := drawerPageRows
	if room < n {
		n = room
	}
	if n < 1 {
		n = 1
	}
	return n
}

// nextSelectable finds the next selectable row from i in direction dir (+1/-1),
// or i itself when there is none.
func (d *drawer) nextSelectable(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(d.rows); j += dir {
		if d.rows[j].selectable() {
			return j
		}
	}
	if i >= 0 && i < len(d.rows) && d.rows[i].selectable() {
		return i
	}
	// Nothing in that direction: find the first selectable row at all.
	for j := 0; j < len(d.rows); j++ {
		if d.rows[j].selectable() {
			return j
		}
	}
	return -1
}

// moveSelection is ^N/^P inside a list drawer.
func (d *drawer) moveSelection(dir int, h int) {
	if d.kind != drawerList || d.cursor < 0 {
		return
	}
	if n := d.nextSelectable(d.cursor, dir); n != d.cursor {
		d.cursor = n
	}
	d.scrollToCursorIn(d.visible(h))
}

func (d *drawer) scrollToCursor() { d.scrollToCursorIn(drawerPageRows) }

// scrollToCursorIn keeps the selected row inside the visible window.
func (d *drawer) scrollToCursorIn(n int) {
	if d.cursor < 0 {
		return
	}
	if d.cursor < d.top {
		d.top = d.cursor
	}
	if d.cursor >= d.top+n {
		d.top = d.cursor - n + 1
	}
	if d.top < 0 {
		d.top = 0
	}
}

// selected is the row under the cursor, if any.
func (d *drawer) selected() (drawerRow, bool) {
	if d.kind != drawerList || d.cursor < 0 || d.cursor >= len(d.rows) {
		return drawerRow{}, false
	}
	return d.rows[d.cursor], true
}

// removeSelected drops the selected row and keeps the selection sensible: what
// `x` on a queued message does to the list it was addressing.
func (d *drawer) removeSelected() {
	if d.cursor < 0 || d.cursor >= len(d.rows) {
		return
	}
	d.rows = append(d.rows[:d.cursor], d.rows[d.cursor+1:]...)
	d.cursor = d.nextSelectable(d.cursor-1, 1)
	d.scrollToCursor()
}

// lines renders the drawer's body: the title, the visible page, and a marker at
// each end saying what is out of view. THE MARKERS ARE NOT DECORATION -- a list
// that silently shows ten of seven hundred is a list that lies about the size of
// the thing you are looking at.
func (d *drawer) lines(w, h int) []string {
	if !d.open() {
		return nil
	}
	if d.kind == drawerLive {
		rows := d.live.Rows(w, d.visible(h))
		for i, r := range rows {
			rows[i] = drawerGray(clipToWidth(r, w))
		}
		return rows
	}
	n := d.visible(h)
	if d.top > max(len(d.rows)-n, 0) {
		d.top = max(len(d.rows)-n, 0)
	}
	out := make([]string, 0, n+3)
	if d.title != "" {
		out = append(out, drawerGray(clipToWidth(d.title, w)))
	}
	if d.top > 0 {
		out = append(out, drawerGray(clipToWidth(fmt.Sprintf("  … %d more", d.top), w)))
	}
	end := min(d.top+n, len(d.rows))
	for i := d.top; i < end; i++ {
		out = append(out, d.rowLine(i, w))
	}
	if end < len(d.rows) {
		out = append(out, drawerGray(clipToWidth(fmt.Sprintf("  … %d more", len(d.rows)-end), w)))
	}
	return out
}

// rowLine draws one row, with the selection cue in the gutter the transcript
// already uses for the same purpose.
func (d *drawer) rowLine(i, w int) string {
	r := d.rows[i]
	// THE WHOLE DRAWER IS THE DULLER GRAY, highlight included. A drawer is
	// furniture beside the conversation, not part of it, so it reads at one
	// remove -- and the selected row is that same gray on a wash rather than
	// full reverse video, which was loud enough to look like an error.
	//
	// The text is STRIPPED of its own colour first. Command output arrives with
	// its own SGR (`ls` colours its tree), and an outer gray wrapped around an
	// inner colour is simply the inner colour: which is why the drawer "doesn't
	// seem any different" from the transcript.
	text := stripSGR(r.text)
	if i == d.cursor {
		return drawerSelected(clipToWidth(padTo(" ▸ "+text, w), w))
	}
	if d.cursor >= 0 && r.selectable() {
		return drawerGray(clipToWidth("   "+text, w))
	}
	return drawerGray(clipToWidth(text, w))
}

// drawerGray is the drawer's one voice: fujiGray, quieter than the transcript.
func drawerGray(s string) string { return term.Label(s) }

// drawerSelected is the same gray on a wash: present without shouting.
func drawerSelected(s string) string {
	if !term.Enabled() {
		return s
	}
	return "\x1b[48;5;237m" + term.Label(s) + "\x1b[49m"
}

// stripSGR removes every escape sequence from s, so the drawer can impose its
// own voice on text that arrived with one.
func stripSGR(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				for j++; j < len(s) && (s[j] < 0x40 || s[j] > 0x7e); j++ {
				}
			}
			i = min(j+1, len(s))
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// hint is what the drawer says about its own dismissal, drawn at the right of
// its closing rule. Every drawer says how to leave, because the one thing every
// reader of a drawer eventually wants is out.
func (d *drawer) hint() string {
	switch {
	case !d.open():
		return ""
	case d.flash != "":
		return d.flash
	case d.kind == drawerList && d.cursor >= 0:
		return "^N/^P select · y yank · Esc close"
	case d.kind == drawerLive:
		return d.live.Hint() + " · Esc close"
	case d.kind == drawerInput:
		return ""
	default:
		return "Esc close"
	}
}

// closingRule is the drawer's bottom edge: a plain rule carrying the hint.
func closingRule(w int, hint string) string {
	if hint == "" {
		return drawerGray(strings.Repeat("─", max(w, 0)))
	}
	right := " " + hint + " ───"
	fill := w - runewidth.StringWidth(right)
	if fill < 3 {
		fill = 3
	}
	return drawerGray(clipToWidth(strings.Repeat("─", fill)+right, w))
}
