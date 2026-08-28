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
		rows[i] = pitGray(clipToWidth(r, w))
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
func (d *pit) moveSelection(dir int, h int) { d.motion(h, func(p *picker) { p.pick(dir) }) }

// scrollBy is j/k and the arrow cluster: READ. It moves the window by a row and
// leaves the selection where the reader put it -- a form with a hundred
// properties is read by scrolling, and dragging the cursor down every line of
// it is how `j` came to skip whole properties.
func (d *pit) scrollBy(dir int, h int) { d.motion(h, func(p *picker) { p.step(dir) }) }

func (d *pit) halfPage(dir int, h int) { d.motion(h, func(p *picker) { p.half(dir) }) }

func (d *pit) toTop()    { d.motion(0, (*picker).home) }
func (d *pit) toBottom() { d.motion(0, (*picker).end) }

// motion runs one list motion against whatever is open, telling the picker how
// tall it was last drawn so a key pressed between paints pages by the right
// amount. A pit with no list takes no motions, and says so by doing nothing.
func (d *pit) motion(h int, f func(*picker)) {
	p := d.list()
	if p == nil {
		return
	}
	if h > 0 {
		p.height = clampInt(h, 1, pickerRows)
	}
	f(p)
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

func (d *pit) removeSelected() { d.motion(0, (*picker).remove) }

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
	out = append(out, pitGray(clipToWidth(d.title, w)))
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

// stripSGR removes every escape sequence from s, so the pit can impose its own
// voice on text that arrived with one. Command output carries its own SGR (`ls`
// colours its tree), and an outer gray wrapped around an inner colour is simply
// the inner colour -- which is why the pit "did not seem any different" from
// the transcript.
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
