package cli

// THE PIT: the pager's one transient region.
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
//	┌ the transcript's rule ───────────────────────────────── 1–29/118 live
//	│   item 1                        ← the PIT: what is open, and its rows
//	│ ♭ item 2                        ← at most one selected row
//	│   item 3
//	│ … 749 more                      ← a page marker, never a silent truncation
//	└ 𝄚 · ✓ · 3b7aff0a · mantra              9.8k/1.0m   ← THE BAR, INVIOLABLE
//
// Everything that is not the transcript and not the status bar is the pit:
// help, figaro status, the queue, a command's output, a hosted verb, an error,
// and the command line itself. One at a time, Esc closes it, and nothing else
// may write to the status row ever again.
//
// WHAT THE PIT HOLDS IS AN ACT, and asking what KIND of thing is open is no
// longer a question the pager can ask. There used to be a `drawerKind` tag --
// none/message/list/input/live -- consulted in eight places, and every one of
// those consultations was a fact the pit could have answered from what it was
// holding. Two of them were the bugs of 2026-08-28: `selected()` answered only
// for a "list", so a hosted form drew a highlight that Enter could not see, and
// one branch checked a tag no code ever set.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/term"
)

// act is what occupies the pit: something that draws itself into w columns and
// EXACTLY h rows, under a pit identity that decides its selection glyph.
//
// There are two, and there is no third: a picker (a list you read and choose
// in) and a screen (a hosted verb that renders itself, for a live view with no
// list to hand over). Anything else the pit ever shows arrives as rows, which
// makes it a picker.
type act interface {
	lines(id pitID, w, h int) []string
}

// screen adapts a self-rendering view (cmdkit.ScreenView) to the pit. A view
// that implements itemView never reaches here: the pit takes its rows instead,
// so that every motion, marker and page count is the picker's, once.
type screen struct{ v cmdkit.ScreenView }

func (s screen) lines(_ pitID, w, h int) []string {
	rows := s.v.Rows(w, h)
	for i, r := range rows {
		rows[i] = pitGray(clipToWidth(pitText(r), w))
	}
	return rows
}

// pitRow is one line of a list, plus what a selection ACTION would operate on.
// text is what is drawn; yank is what `y` copies; id is what a verb key (`x` on
// a queued message) addresses. A row with an empty yank and id is chrome -- a
// header, a blank, a page marker -- and cannot be selected.
type pitRow struct {
	text string
	yank string
	id   string
}

// staticRow is a row that is drawn but never selected: a header, a blank, a
// page marker. (Not "chromeRow": transcript_mouse_test.go already owns that
// name for a different idea -- a painted row belonging to no node.)
func staticRow(text string) pitRow { return pitRow{text: text} }

func (r pitRow) selectable() bool { return r.yank != "" || r.id != "" }

// pit is the region itself. The zero value is closed.
type pit struct {
	// id is WHICH pit this is -- "queue", "help", "form show" -- and through
	// pitid.go it is also the glyph on the bar, the keymode, and the glyph on
	// the selected row. It is the pit's whole identity; nothing else names it.
	id    pitID
	title string // drawn as the pit's first row when non-empty

	// body is what is on screen: a picker, or a hosted view's own screen.
	body act
	// live is the hosted verb, when there is one. The pit forwards it the keys
	// that are its own and closes it on the way out; it knows nothing about
	// pits and the pit knows nothing about forms. See cmdkit.LiveView.
	live cmdkit.LiveView
	// full is fullscreen: the pit takes the pane and the transcript is shadowed
	// behind it. Cleared when the pit closes, because a fullscreen pit that is
	// not open is a screen with nothing on it.
	full bool
}

func (d *pit) open() bool { return d.body != nil }

// list is the pit's picker, or nil when what is open renders itself. Every
// motion goes through here, so a hosted screen simply takes none of them.
func (d *pit) list() *picker {
	p, _ := d.body.(*picker)
	return p
}

// glance reports a pit that is READ AT A GLANCE rather than navigated: one
// line, no list to move in, and therefore dismissed by any key. It is the note
// pit and nothing else.
func (d *pit) glance() bool { return d.id == pitNote }

// close empties the pit, AND RELEASES WHAT IT WAS HOSTING. A live view holds a
// subscription and a socket; four `:form show`s and four Escs used to leave
// four of each behind, every one of them still asking the pager to repaint on
// every delta of a form nobody is looking at.
func (d *pit) close() {
	if d.live != nil {
		d.live.Close()
	}
	*d = pit{}
}

// itemView is a live view that has a LIST rather than a screen. The pit takes
// its rows and drives them with the picker; the view keeps only the verbs that
// are its own. A view that does not implement this still renders itself.
type itemView interface {
	Items(width int) []pitRow
	Activate(path string)
}

// showLive hosts a verb. The pit owns the region and the dismissal; the view
// owns what is inside it -- unless it has a list, in which case it hands the
// rows over and keeps only its own verbs.
func (d *pit) showLive(id pitID, v cmdkit.LiveView) {
	if d.live != nil && d.live != v {
		d.live.Close() // one pit, one hosted view
	}
	d.id, d.title, d.live = id, "", v
	switch view := v.(type) {
	case itemView:
		d.body = newPicker(view.Items(80))
	case cmdkit.ScreenView:
		d.body = screen{view}
	default:
		// A HOSTED VERB THAT IS NEITHER A LIST NOR A SCREEN has nothing to
		// show, and saying so beats an empty region with a glyph over it.
		d.body = newPicker([]pitRow{staticRow("  " + string(id) + ": nothing to render")})
	}
}

// refreshLive re-reads an itemised view's rows. The cursor stays on the same
// PATH rather than the same index -- a form that grows a key under the cursor
// must not move the selection out from under it -- and that is the picker's
// job now, so this is a row swap and nothing else.
func (d *pit) refreshLive(w int) {
	iv, ok := d.live.(itemView)
	if !ok || d.list() == nil {
		return
	}
	d.list().setRows(iv.Items(w), "")
}

// showNote is the one-line pit: an error, a result, a note too long for the
// bar. Any key wipes it -- see glance().
func (d *pit) showNote(text string) {
	d.showList(pitNote, "", []pitRow{staticRow("  " + text)})
}

// showList opens a list. Whether it has a cursor is the ROWS' answer: help and
// status are built of staticRow and get none; the queue and command output
// carry ids and do.
func (d *pit) showList(id pitID, title string, rows []pitRow) {
	if d.live != nil {
		d.live.Close()
	}
	d.id, d.title, d.live = id, title, nil
	d.body = newPicker(rows)
}

// visible is how many rows fit: the fixed page, bounded by what the pane can
// actually give. h is the whole pane.
func (d *pit) visible(h int) int {
	// FULLSCREEN TAKES THE PANE, minus the rule above the pit and the status
	// bar below it. The reservation is the same arithmetic either way -- what
	// changes is only whether pickerRows caps it.
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
	n := pickerRows
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
func (d *pit) toggleFull() {
	if d.open() {
		d.full = !d.full
	}
}

// THE LIST BEHAVIOURS ARE THE PICKER'S. What used to be six methods here --
// nextSelectable, moveSelection, scrollToCursor, scrollToCursorIn, selected,
// removeSelected -- was a second implementation of what the completion menu
// already did, differing from it in the marker it drew and in nothing else.
// These forward; picker.go decides.

// moveSelection is ^N/^P: choose.
func (d *pit) moveSelection(dir int) { d.motion(func(p *picker) { p.pick(dir) }) }

// scrollBy is j/k and the arrow cluster: READ. It moves the window by a row and
// leaves the selection where the reader put it -- a form with a hundred
// properties is read by scrolling, and dragging the cursor down every line of
// it is how `j` came to skip whole properties.
func (d *pit) scrollBy(dir int) { d.motion(func(p *picker) { p.step(dir) }) }

func (d *pit) halfPage(dir int) { d.motion(func(p *picker) { p.half(dir) }) }

func (d *pit) toTop()    { d.motion((*picker).home) }
func (d *pit) toBottom() { d.motion((*picker).end) }

// motion runs one list motion against whatever is open. A pit with no list
// takes no motions, and says so by doing nothing.
//
// THE HEIGHT IS THE PICKER'S OWN, and passing one in here was a bug you could
// feel: the pit handed over its GROSS height (twelve rows), while the picker
// draws two of those rows as the "… N more" markers and keeps ten for the
// list. follow() then kept the cursor inside a window two rows taller than the
// one on screen, so pressing k on the last row moved the selection off the
// bottom of what was painted: the highlight appeared stuck on the last visible
// row and the row you had just left vanished behind the marker. The picker
// measures itself every time it draws (picker.lines); nobody else may.
func (d *pit) motion(f func(*picker)) {
	if p := d.list(); p != nil {
		f(p)
	}
}

// selected is the row under the cursor, in WHATEVER pit is open. It used to
// answer only for a "list" pit, which is how a hosted form came to have a
// highlight that Enter and `y` could not see: the pit drew the picker's cursor
// and then told every verb there was no selection.
func (d *pit) selected() (pitRow, bool) {
	if p := d.list(); p != nil {
		return p.selected()
	}
	return pitRow{}, false
}

// replaceRows swaps the list under a live cursor. See picker.setRows: the
// window, the height and the selection are the picker's to keep.
func (d *pit) replaceRows(rows []pitRow, keepID string) {
	if p := d.list(); p != nil {
		p.setRows(rows, keepID)
		return
	}
	d.body = newPicker(rows)
}

func (d *pit) removeSelected() { d.motion((*picker).remove) }

func (d *pit) lines(w, h int) []string {
	if !d.open() {
		return nil
	}
	if d.live != nil {
		d.refreshLive(w) // the view is live: its rows change under the pit
	}
	room := d.visible(h)
	if d.title == "" {
		return d.body.lines(d.id, w, room)
	}
	out := make([]string, 0, room+1)
	out = append(out, pitGray(clipToWidth(pitText(d.title), w)))
	return append(out, d.body.lines(d.id, w, room-1)...)
}

// pitGray is the pit's one voice: fujiGray, quieter than the transcript. The
// WHOLE pit reads at one remove -- it is furniture beside the conversation,
// not part of it.
func pitGray(s string) string { return term.Label(s) }

// pitSelected is the same gray on a wash: present without shouting. Full
// reverse video was loud enough to look like an error.
func pitSelected(s string) string {
	if !term.Enabled() {
		return s
	}
	return "\x1b[48;5;237m" + term.Label(s) + "\x1b[49m"
}

// pitText is THE GATE EVERY PIT ROW PASSES THROUGH, and it takes out more
// than colour.
//
// A pit shows text that came from somewhere else: a form value, a command's
// captured output, a queued message. All three can carry ESCAPE SEQUENCES --
// a form holds tool output, and tool output holds whatever the tool printed.
// Measured by a reviewer against this branch: a value containing
// "\x1b[2J\x1b[10A" opened in the form pit CLEARED THE PANE -- fifteen painted
// rows became one, the rule and the head row never came back, and the next
// keystroke left a blank screen. The row was clipped and greyed on the way
// out; neither of those looks at what is inside it.
//
// So the rule is: the pit paints TEXT, never control. Escape sequences go
// (CSI, OSC and the two-byte forms alike), the other C0 controls go, DEL goes,
// and a tab becomes a space -- because a tab in a windowed list is a jump to a
// column the list does not own. Whatever colour the pit wants, it puts on
// AFTERWARDS: an outer grey wrapped around an inner colour is just the inner
// colour, which is why command output never looked like it was in a pit.
func pitText(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			switch {
			case c == '\t':
				b.WriteByte(' ')
			case c < 0x20 || c == 0x7f:
				// dropped: a newline inside a ROW is not a row break, it is a
				// row the reader cannot see the end of
			default:
				b.WriteByte(c)
			}
			i++
			continue
		}
		i = skipEscape(s, i)
	}
	return b.String()
}

func isControl(r rune) bool { return r == 0x1b || r == '\t' || r < 0x20 || r == 0x7f }

// skipEscape returns the index just past the escape sequence beginning at i.
// CSI (ESC [ … final) and OSC (ESC ] … BEL or ST) are consumed whole; anything
// else costs the ESC and the byte after it.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	switch s[j] {
	case '[':
		for j++; j < len(s) && (s[j] < 0x40 || s[j] > 0x7e); j++ {
		}
	case ']':
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				break
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				j++
				break
			}
		}
	}
	return min(j+1, len(s))
}
