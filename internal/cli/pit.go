package cli

// The pit is the pager's one transient region: one at a time, Esc closes it, and
// none of them may touch the status bar.

import (
	"math"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/term"
)

type act interface {
	lines(id pitID, w, h int) []string
}

type screen struct{ v cmdkit.ScreenView }

func (s screen) lines(_ pitID, w, h int) []string {
	rows := s.v.Rows(w, h)
	for i, r := range rows {
		rows[i] = pitGray(clipToWidth(pitText(r), w))
	}
	return rows
}

// A row with neither yank nor id is chrome and cannot be selected.
type pitRow struct {
	text     string
	textFrom interface{ rowText() string }
	yank     string
	yankFrom interface{ yankText() string }
	id       string
	aria     string
	// note is drawn at full strength on the selected row: dim on a wash would
	// not read.
	note string
	hint string
	// tone cannot ride in the text: pitText strips escapes, and must.
	tone    pitTone
	marks   []int
	dimLead int
}

type pitTone uint8

const (
	toneNone pitTone = iota
	toneError
	toneWarn
)

func staticRow(text string) pitRow { return pitRow{text: text} }

func (r pitRow) selectable() bool { return r.yank != "" || r.yankFrom != nil || r.id != "" }

func (r pitRow) str() string {
	if r.textFrom != nil {
		return r.textFrom.rowText()
	}
	return r.text
}

func (r pitRow) yankText() string {
	if r.yankFrom != nil {
		return r.yankFrom.yankText()
	}
	return r.yank
}

type pit struct {
	id    pitID
	title string

	liveRev   uint64
	liveWidth int

	body act
	live cmdkit.LiveView
	// full is the pager's disposition (transcript.full), handed down on every
	// paint.
	full bool
}

func (d *pit) open() bool { return d.body != nil }

func (d *pit) list() *picker {
	p, _ := d.body.(*picker)
	return p
}

func (d *pit) glance() bool { return d.id == pitNote }

func (d *pit) natural() int {
	p := d.list()
	if p == nil {
		return math.MaxInt
	}
	n := len(p.rows)
	if d.title != "" {
		n++
	}
	return n
}

// A live view holds a subscription and a socket.
func (d *pit) close() {
	if d.live != nil {
		d.live.Close()
	}
	*d = pit{}
}

type itemView interface {
	Items(width int) []pitRow
	Activate(path string)
}

type treeView interface {
	Expand(id string)
	Collapse(id string) string
}

func (d *pit) showLive(id pitID, v cmdkit.LiveView) {
	if d.live != nil && d.live != v {
		d.live.Close()
	}
	d.id, d.title, d.live = id, "", v
	switch view := v.(type) {
	case itemView:
		d.body = newPicker(view.Items(80))
		d.liveRev, d.liveWidth = 0, 0
		if rv, ok := v.(revisioned); ok {
			d.liveRev, d.liveWidth = rv.Revision(), 80
		}
	case cmdkit.ScreenView:
		d.body = screen{view}
	default:
		d.body = newPicker([]pitRow{staticRow("  " + string(id) + ": nothing to render")})
	}
}

type revisioned interface{ Revision() uint64 }

func (d *pit) refreshLive(w int) {
	iv, ok := d.live.(itemView)
	if !ok || d.list() == nil {
		return
	}
	rows := iv.Items(w)
	if rv, ok := d.live.(revisioned); ok {
		rev := rv.Revision()
		if rev == d.liveRev && w == d.liveWidth {
			return
		}
		d.liveRev, d.liveWidth = rev, w
	}
	d.list().setRows(rows, "")
}

func (d *pit) showNote(text string) {
	d.showList(pitNote, "", []pitRow{staticRow("  " + text)})
}

func (d *pit) showList(id pitID, title string, rows []pitRow) {
	if d.live != nil {
		d.live.Close()
	}
	d.id, d.title, d.live = id, title, nil
	d.body = newPicker(rows)
}

func (d *pit) focus(id string) bool {
	if p := d.list(); p != nil {
		return p.focus(id)
	}
	return false
}

func (d *pit) findRow(q string, dir int) bool {
	p := d.list()
	if p == nil || q == "" {
		return false
	}
	return p.find(q, dir)
}

// room comes from transcript.pitRoom; the pit must not compute it, since the bar
// is three rows when it wraps.
func (d *pit) visible(room int) int {
	if d.title != "" {
		room--
	}
	n := pickerRows
	if d.full {
		n = room
	}
	return max(min(n, room), 1)
}

func (d *pit) moveSelection(dir int) { d.motion(func(p *picker) { p.pick(dir) }) }

func (d *pit) scrollBy(dir int) { d.motion(func(p *picker) { p.step(dir) }) }

func (d *pit) halfPage(dir int) { d.motion(func(p *picker) { p.half(dir) }) }

func (d *pit) toTop()    { d.motion((*picker).home) }
func (d *pit) toBottom() { d.motion((*picker).end) }

// The height is the picker's own: it measures itself when it draws.
func (d *pit) motion(f func(*picker)) {
	if p := d.list(); p != nil {
		f(p)
	}
}

func (d *pit) selected() (pitRow, bool) {
	if p := d.list(); p != nil {
		return p.selected()
	}
	return pitRow{}, false
}

func (d *pit) neighbour() (pitRow, bool) {
	if p := d.list(); p != nil {
		return p.neighbour()
	}
	return pitRow{}, false
}

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
		d.refreshLive(w)
	}
	h = max(h, 1)
	out := make([]string, 0, h)
	body := h
	if d.title != "" {
		out = append(out, pitGray(clipToWidth(pitText(d.title), w)))
		body--
	}
	out = append(out, d.body.lines(d.id, w, body)...)
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

func pitGray(s string) string { return term.Label(s) }

func (r pitRow) dims() (lead, tail int) {
	if d, ok := r.textFrom.(interface{ rowDims() (int, int) }); ok {
		return d.rowDims()
	}
	return r.dimLead, 0
}

func paintRow(s string, lead, tail int, marks []int, back, match string) string {
	if (lead == 0 && tail == 0 && len(marks) == 0) || !term.Enabled() {
		return s
	}
	on := make(map[int]bool, len(marks))
	for _, m := range marks {
		if m >= 0 && m < len(s) {
			on[m] = true
		}
	}
	style := func(i int) (dim, mark bool) {
		return i < lead || (tail > 0 && i >= tail), on[i]
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		dim, mark := style(i)
		j := i + 1
		for j < len(s) {
			d, m := style(j)
			if d != dim || m != mark {
				break
			}
			j++
		}
		run := s[i:j]
		switch {
		case mark:
			run = match + "\x1b[1;4m" + run + "\x1b[22;24m" + back
		case dim:
			run = term.LabelThen(run, back)
		}
		b.WriteString(run)
		i = j
	}
	return b.String()
}

// The selected row is full strength over the wash; a toned row keeps its colour.
func pitSelected(s string, tone pitTone, w int) string {
	if !term.Enabled() {
		return s
	}
	return "\x1b[48;5;240m" + tone.selectedFg() + s + strings.Repeat(" ", max(w-displayWidth(s), 0)) + "\x1b[39m\x1b[49m"
}

func (t pitTone) selectedFg() string {
	if body := t.washBody(); body != "" {
		return body
	}
	return "\x1b[38;5;255m"
}

func matchFg(selected bool) string {
	if selected {
		return term.NoticeOnWash()
	}
	return term.NoticeBody()
}

func (t pitTone) fg(selected bool) string {
	if selected {
		return t.selectedFg()
	}
	switch t {
	case toneError:
		return term.NoticeBody()
	case toneWarn:
		return term.CautionBody()
	}
	return "\x1b[39m"
}

// An untoned row is returned untouched.
func (t pitTone) paint(s string) string {
	body := ""
	switch t {
	case toneError:
		body = term.NoticeBody()
	case toneWarn:
		body = term.CautionBody()
	}
	if body == "" {
		return s
	}
	return body + s + "\x1b[0m"
}

// The palette's own red on the mid-grey wash is 1.9:1, so the tone is lifted.
func (t pitTone) washBody() string {
	switch t {
	case toneError:
		return term.NoticeOnWash()
	case toneWarn:
		return term.CautionOnWash()
	}
	return ""
}

// The pit paints text, never control: rows carry tool output, and "\x1b[2J" in one
// cleared the pane. Escapes, C0 and DEL go; tab becomes a space.
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
				// dropped: a newline inside a row is not a row break
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

// CSI and OSC are skipped whole; anything else is ESC and one byte.
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
