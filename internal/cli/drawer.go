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
	// full is fullscreen: the pit takes the pane and the transcript is shadowed
	// behind it. Cleared when the pit closes, because a fullscreen pit that is
	// not open is a screen with nothing on it.
	full bool
	// pick is the LIST, and every behaviour of one: the window, the cursor,
	// the motions and the truncation marker. The drawer used to own a second
	// implementation of all four (see picker.go for the three that existed).
	// It is nil only for a drawer with no rows -- a message, or a hosted live
	// view that renders itself.
	pick *picker
}

func (d *drawer) open() bool { return d.kind != drawerNone }

// close empties the pit, AND RELEASES WHAT IT WAS HOSTING. A live view holds a
// subscription and a socket; four `:form show`s and four Escs used to leave
// four of each behind, every one of them still asking the pager to repaint on
// every delta of a form nobody is looking at.
func (d *drawer) close() {
	if d.live != nil {
		d.live.Close()
	}
	*d = drawer{}
}

// showLive hosts a verb that renders itself. The drawer owns the region and
// the dismissal; the view owns everything inside it.
// itemView is a live view that has a LIST rather than a screen. The pit takes
// its rows and drives them with the picker; the view keeps only the verbs that
// are its own. A view that does not implement this still renders itself.
type itemView interface {
	Items(width int) []drawerRow
	Activate(path string)
}

func (d *drawer) showLive(name string, v cmdkit.LiveView) {
	if d.live != nil && d.live != v {
		d.live.Close() // one pit, one hosted view
	}
	d.kind, d.name, d.title = drawerLive, name, ""
	d.flash, d.pick = "", nil
	d.live = v
	if iv, ok := v.(itemView); ok {
		d.pick = newPicker(iv.Items(80))
	}
}

// refreshLive re-reads an itemised view's rows. The cursor stays on the same
// PATH rather than the same index -- a form that grows a key under the cursor
// must not move the selection out from under it -- and that is the picker's
// job now, so this is a row swap and nothing else.
func (d *drawer) refreshLive(w int) {
	iv, ok := d.live.(itemView)
	if !ok || d.pick == nil {
		return
	}
	d.pick.setRows(iv.Items(w), "")
}

// showMessage is the one-line drawer: an error, a result, a note. It carries
// its own dismissal hint because a message that cannot be dismissed is a
// message that has taken the screen hostage.
func (d *drawer) showMessage(text string) {
	d.kind, d.name, d.title = drawerMessage, "message", ""
	d.flash, d.live = "", nil
	d.pick = newPicker([]drawerRow{staticRow("  " + text)})
}

// showList opens a list drawer. Whether it has a cursor is the ROWS' answer:
// help and status are built of staticRow and get none; the queue and command
// output carry ids and do.
func (d *drawer) showList(name, title string, rows []drawerRow) {
	d.kind, d.name, d.title = drawerList, name, title
	d.flash, d.live = "", nil
	d.pick = newPicker(rows)
}

// visible is how many rows of the list fit: the fixed page, bounded by what the
// pane can actually give. h is the whole pane.
func (d *drawer) visible(h int) int {
	// FULLSCREEN TAKES THE PANE, minus the rule above the pit and the status
	// bar below it. The reservation is the same arithmetic either way -- what
	// changes is only whether drawerPageRows caps it.
	room := h - 3 - 3
	if d.full {
		// THE RULE AND THE BAR ARE NOT THE PIT'S TO TAKE. Fullscreen claims
		// everything else: h, minus the rule above it, minus the bar below.
		// Asking for one row more made the stanza taller than the pane, and
		// the placement -- which refuses a negative origin rather than
		// painting a stanza that would not fit -- skipped it entirely, so
		// `F` blanked the screen.
		room = h - 3
	}
	if d.title != "" {
		room--
	}
	n := drawerPageRows
	if d.full {
		n = room
	}
	if room < n {
		n = room
	}
	if n < 1 {
		n = 1
	}
	return n
}

// toggleFull is 'F': the pit takes the whole pane, with the transcript
// shadowed behind it rather than scrolled away. It is a property of the PIT,
// not of any one list, which is why it lives here and works for help, the
// queue, notifications and a form alike -- the thing the picker consolidation
// bought.
func (d *drawer) toggleFull() {
	if d.open() {
		d.full = !d.full
	}
}

// nextSelectable finds the next selectable row from i in direction dir (+1/-1),
// or i itself when there is none.
// THE LIST BEHAVIOURS ARE THE PICKER'S. What used to be six methods here --
// nextSelectable, moveSelection, scrollToCursor, scrollToCursorIn, selected,
// removeSelected -- was a second implementation of what the completion menu
// already did, differing from it in the marker it drew and in nothing else.
// These forward; picker.go decides.

// moveSelection is ^N/^P: choose.
func (d *drawer) moveSelection(dir int, h int) {
	if d.pick == nil {
		return
	}
	d.pick.height = clampInt(h, 1, pickerRows)
	d.pick.pick(dir)
}

// scrollBy is j/k and the arrow cluster: READ. It moves the window by a row and
// leaves the selection where the reader put it -- a form with a hundred
// properties is read by scrolling, and dragging the cursor down every line of
// it is how `j` came to skip whole properties.
func (d *drawer) scrollBy(dir int, h int) {
	if d.pick == nil {
		return
	}
	d.pick.height = clampInt(h, 1, pickerRows)
	d.pick.step(dir)
}

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

// selected is the row under the cursor, in WHATEVER pit is open. It used to
// answer only for a drawerList, which is how a hosted form came to have a
// highlight that Enter and `y` could not see: the pit drew the picker's cursor
// and then told every verb there was no selection.
func (d *drawer) selected() (drawerRow, bool) {
	if d.pick == nil {
		return drawerRow{}, false
	}
	return d.pick.selected()
}

// replaceRows swaps the list under a live cursor. See picker.setRows: the
// window, the height and the selection are the picker's to keep.
func (d *drawer) replaceRows(rows []drawerRow, keepID string) {
	if d.pick == nil {
		d.pick = newPicker(rows)
		return
	}
	d.pick.setRows(rows, keepID)
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
	if d.kind == drawerLive && d.pick == nil {
		rows := d.live.Rows(w, d.visible(h))
		for i, r := range rows {
			rows[i] = drawerGray(clipToWidth(r, w))
		}
		return rows
	}
	if d.kind == drawerLive {
		d.refreshLive(w) // the view is live: its rows change under the pit
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
