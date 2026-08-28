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
	// pick is the LIST, and every behaviour of one: the window, the cursor,
	// the motions and the truncation marker. The drawer used to own a second
	// implementation of all four (see picker.go for the three that existed).
	// It is nil only for a drawer with no rows -- a message, or a hosted live
	// view that renders itself.
	pick *picker
}

func (d *drawer) open() bool { return d.kind != drawerNone }

// close empties the drawer. Esc reaches here from every kind.
func (d *drawer) close() {
	*d = drawer{}
}

// showLive hosts a verb that renders itself. The drawer owns the region and
// the dismissal; the view owns everything inside it.
func (d *drawer) showLive(name string, v cmdkit.LiveView) {
	d.kind, d.name, d.title = drawerLive, name, ""
	d.flash, d.pick = "", nil
	d.live = v
}

// showMessage is the one-line drawer: an error, a result, a note. It carries
// its own dismissal hint because a message that cannot be dismissed is a
// message that has taken the screen hostage.
func (d *drawer) showMessage(text string) {
	d.kind, d.name, d.title = drawerMessage, "message", ""
	d.flash, d.live = "", nil
	d.pick = newPicker([]drawerRow{staticRow("  " + text)}, false)
}

// showList opens a list drawer, selecting the first selectable row when the
// caller asked for selection.
func (d *drawer) showList(name, title string, rows []drawerRow, selectable bool) {
	d.kind, d.name, d.title = drawerList, name, title
	d.flash, d.live = "", nil
	d.pick = newPicker(rows, selectable)
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
// THE LIST BEHAVIOURS ARE THE PICKER'S. What used to be six methods here --
// nextSelectable, moveSelection, scrollToCursor, scrollToCursorIn, selected,
// removeSelected -- was a second implementation of what the completion menu
// already did, differing from it in the marker it drew and in nothing else.
// These forward; picker.go decides.

func (d *drawer) moveSelection(dir int, h int) {
	if d.pick == nil {
		return
	}
	d.pick.height = clampInt(h, 1, pickerRows)
	d.pick.move(dir)
}

// scrollBy is j/k and the arrow cluster: it moves the SELECTION in a drawer
// that has one and the WINDOW in a drawer that does not, which is what makes
// `j` mean one thing to a reader across every drawer.
func (d *drawer) scrollBy(dir int, h int) { d.moveSelection(dir, h) }

func (d *drawer) halfPage(dir int, h int) {
	if d.pick == nil {
		return
	}
	d.pick.height = clampInt(h, 1, pickerRows)
	d.pick.half(dir)
}

func (d *drawer) toTop() {
	if d.pick != nil {
		d.pick.home()
	}
}
func (d *drawer) toBottom() {
	if d.pick != nil {
		d.pick.end()
	}
}

func (d *drawer) selected() (drawerRow, bool) {
	if d.kind != drawerList || d.pick == nil {
		return drawerRow{}, false
	}
	return d.pick.selected()
}

// replaceRows swaps the list under a live cursor, keeping the selection ON THE
// SAME ROW ID rather than at the same index: a queue that re-sorts under the
// cursor on every poll is a queue nobody can hit `x` on, and an index-keyed
// restore is how `x` came to drop the wrong message.
func (d *drawer) replaceRows(rows []drawerRow, keepID string) {
	if d.pick == nil {
		d.pick = newPicker(rows, true)
		return
	}
	sel := d.pick.selectable()
	top := d.pick.top
	d.pick = newPicker(rows, sel)
	d.pick.top = top
	if keepID != "" {
		for i, r := range rows {
			if r.id == keepID {
				d.pick.cursor = i
				break
			}
		}
	}
	d.pick.follow()
}

func (d *drawer) removeSelected() {
	if d.pick != nil {
		d.pick.remove()
	}
}

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
	out := make([]string, 0, d.visible(h)+3)
	if d.title != "" {
		out = append(out, drawerGray(clipToWidth(d.title, w)))
	}
	if d.pick == nil {
		return out
	}
	// EVERY LIST IS A PICKER. The window, the cursor, the selection marker and
	// the "… N more" on both edges come from one place; what a drawer still
	// owns is its rows and its verb keys.
	return append(out, d.pick.lines(drawerID(d.name), w, d.visible(h)-len(out))...)
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
