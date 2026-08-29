package cli

// THE PIT: the pager's one transient region -- help, status, the queue, a
// command's output, a hosted verb, a note. One at a time, Esc closes it, and
// none of them may touch the status bar.
//
//	┌ the transcript's rule ───────────────────────────────── 1–29/118 live
//	│   item 1
//	│ ♭ item 2                        ← at most one selected row
//	│ … 749 more                      ← never a silent truncation
//	└ 𝄚 · ✓ · 3b7aff0a · mantra              9.8k/1.0m   ← the bar, inviolable
//
// The pit holds an ACT, and never asks what kind of thing is open: everything
// that question used to decide is answered by what it is holding.

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/term"
)

// act is what occupies the pit: a picker (a list) or a screen (a hosted view
// that renders itself). There is no third.
type act interface {
	lines(id pitID, w, h int) []string
}

// screen adapts a self-rendering view to the pit. A view with Items() never
// reaches here: the pit takes its rows and drives them with the picker.
type screen struct{ v cmdkit.ScreenView }

func (s screen) lines(_ pitID, w, h int) []string {
	rows := s.v.Rows(w, h)
	for i, r := range rows {
		rows[i] = pitGray(clipToWidth(pitText(r), w))
	}
	return rows
}

// pitRow is one line: text is drawn, yank is what `y` copies, id is what a
// verb key addresses. A row with neither is chrome and cannot be selected.
type pitRow struct {
	text string
	yank string
	id   string
}

// staticRow is drawn but never selected. (Not "chromeRow": that name is taken
// in transcript_mouse_test.go for a different idea.)
func staticRow(text string) pitRow { return pitRow{text: text} }

func (r pitRow) selectable() bool { return r.yank != "" || r.id != "" }

// pit is the region itself. The zero value is closed.
type pit struct {
	// id is which pit this is, and through pitid.go also its glyph, its
	// keymode and its selection marker. Nothing else names a pit.
	id    pitID
	title string // drawn as the pit's first row when non-empty

	body act             // the picker or screen on show
	live cmdkit.LiveView // the hosted verb, when there is one
	// full is handed down by the transcript on every paint: fullscreen is the
	// pager's disposition, not this pit's (see transcript.full).
	full bool
}

func (d *pit) open() bool { return d.body != nil }

// list is the pit's picker, or nil when what is open renders itself: every
// motion goes through here, so a screen takes none of them.
func (d *pit) list() *picker {
	p, _ := d.body.(*picker)
	return p
}

// glance reports a pit that is read at a glance and dismissed by any key.
func (d *pit) glance() bool { return d.id == pitNote }

// close empties the pit and releases what it was hosting: a live view holds a
// subscription and a socket.
func (d *pit) close() {
	if d.live != nil {
		d.live.Close()
	}
	*d = pit{}
}

// itemView is a live view with a LIST rather than a screen: the pit drives the
// rows, the view keeps only the verbs that are its own.
type itemView interface {
	Items(width int) []pitRow
	Activate(path string)
}

// showLive hosts a verb: the pit owns the region and the dismissal.
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

// refreshLive re-reads an itemised view's rows; the picker keeps the cursor on
// the same row id across the swap.
func (d *pit) refreshLive(w int) {
	iv, ok := d.live.(itemView)
	if !ok || d.list() == nil {
		return
	}
	d.list().setRows(iv.Items(w), "")
}

// showNote is the one-line pit: a result too long for the bar. Any key wipes
// it (see glance).
func (d *pit) showNote(text string) {
	d.showList(pitNote, "", []pitRow{staticRow("  " + text)})
}

// showList opens a list. Whether it has a cursor is the rows' answer.
func (d *pit) showList(id pitID, title string, rows []pitRow) {
	if d.live != nil {
		d.live.Close()
	}
	d.id, d.title, d.live = id, title, nil
	d.body = newPicker(rows)
}

// visible is how many rows the list may draw inside the room the pane has
// already been asked for (transcript.pitRoom). It must not compute that room
// itself: the bar is three rows when it wraps, and a pit that assumed one
// overran the pane and lost its top -- cursor and marker with it.
func (d *pit) visible(room int) int {
	if d.title != "" {
		room--
	}
	n := pickerRows
	if d.full {
		n = room // fullscreen takes everything it was given
	}
	return max(min(n, room), 1)
}

// The list behaviours are the picker's; these forward.

// moveSelection is ^N/^P: choose.
func (d *pit) moveSelection(dir int) { d.motion(func(p *picker) { p.pick(dir) }) }

// scrollBy moves the window by a row and leaves the selection alone.
func (d *pit) scrollBy(dir int) { d.motion(func(p *picker) { p.step(dir) }) }

func (d *pit) halfPage(dir int) { d.motion(func(p *picker) { p.half(dir) }) }

func (d *pit) toTop()    { d.motion((*picker).home) }
func (d *pit) toBottom() { d.motion((*picker).end) }

// motion runs one list motion against whatever is open. The height is the
// picker's own -- it measures itself when it draws, and nobody else may.
func (d *pit) motion(f func(*picker)) {
	if p := d.list(); p != nil {
		f(p)
	}
}

// selected is the row under the cursor, in whatever pit is open.
func (d *pit) selected() (pitRow, bool) {
	if p := d.list(); p != nil {
		return p.selected()
	}
	return pitRow{}, false
}

// replaceRows swaps the list under a live cursor; picker.setRows keeps the
// window and the selection.
func (d *pit) replaceRows(rows []pitRow, keepID string) {
	if p := d.list(); p != nil {
		p.setRows(rows, keepID)
		return
	}
	d.body = newPicker(rows)
}

func (d *pit) removeSelected() { d.motion((*picker).remove) }

func (d *pit) lines(w, room int) []string {
	if !d.open() {
		return nil
	}
	if d.live != nil {
		d.refreshLive(w) // the view is live: its rows change under the pit
	}
	full := d.visible(room)
	out := make([]string, 0, full+1)
	body := full
	if d.title != "" {
		out = append(out, pitGray(clipToWidth(pitText(d.title), w)))
		body--
	}
	out = append(out, d.body.lines(d.id, w, body)...)
	if !d.full {
		return out
	}
	// Fullscreen occupies the whole pane whether or not it needs to.
	for len(out) < full {
		out = append(out, "")
	}
	return out
}

// pitGray is the pit's one voice: furniture beside the conversation.
func pitGray(s string) string { return term.Label(s) }

// pitSelected is the one row in a pit drawn at full strength: it is what every
// verb acts on, so it must be read first. Dim text on a dark wash was not.
func pitSelected(s string) string {
	if !term.Enabled() {
		return s
	}
	return "\x1b[48;5;240m\x1b[38;5;255m" + s + "\x1b[39m\x1b[49m"
}

// pitText is the gate every pit row passes through: THE PIT PAINTS TEXT, NEVER
// CONTROL. Rows come from forms, command output and queued messages, and any
// of them can carry escape sequences -- a form holds tool output. A value
// carrying "\x1b[2J" cleared the pane. Escapes, C0 controls and DEL go; a tab
// becomes a space. The pit's own colour is applied afterwards.
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

// skipEscape returns the index just past the escape at i: CSI and OSC whole,
// anything else the ESC and one byte.
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
