package cli

import (
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// A cell-accurate VT for proving paint-layer rewrites are invisible.
//
// ldrender.FakeTerminal models the grid but drops SGR entirely, so it cannot
// tell a coloured cell from a plain one — useless for checking a transform
// whose whole job is rewriting colour. vtScreen is the smaller, sharper tool:
// it keeps (rune, style) per cell for the subset of ANSI paint emits, plus the
// scroll-region operations the shifted-frame path uses.
//
// The contract every test here asserts: replaying the optimized escape stream
// must leave the same *appearance* grid as replaying the naive full-repaint
// stream. Appearance, not bytes: a blank cell is defined by its background and
// the attributes that draw on emptiness, which is exactly the licence
// compactRow takes.
// ---------------------------------------------------------------------------

type vtStyle struct {
	fg, bg                            string
	bold, dim, italic                 bool
	underline, reverse, strike, blink bool
	conceal                           bool
}

type vtCell struct {
	r rune
	s vtStyle
}

// appearance normalizes a cell to what a viewer can actually distinguish.
func (c vtCell) appearance() vtCell {
	if c.r == 0 {
		c.r = ' '
	}
	if c.r == ' ' && !c.s.reverse && !c.s.underline && !c.s.strike {
		return vtCell{r: ' ', s: vtStyle{bg: c.s.bg}}
	}
	return c
}

type vtScreen struct {
	w, h     int
	cells    [][]vtCell
	row, col int
	cur      vtStyle
	top, bot int // scroll region, 0-based inclusive
	pend     []byte
}

func newVT(w, h int) *vtScreen {
	v := &vtScreen{w: w, h: h, top: 0, bot: h - 1}
	v.cells = make([][]vtCell, h)
	for r := range v.cells {
		v.cells[r] = make([]vtCell, w)
	}
	return v
}

func (v *vtScreen) Write(p []byte) (int, error) {
	data := append(v.pend, p...)
	v.pend = nil
	i := 0
	for i < len(data) {
		if data[i] != 0x1b {
			// Columns are display cells, not bytes: a box-drawing rule is three
			// bytes and one column, and the painter's cursor arithmetic has to
			// agree with that or a suffix update lands in the wrong place.
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 && !utf8.FullRune(data[i:]) {
				v.pend = append(v.pend, data[i:]...) // truncated rune
				return len(p), nil
			}
			v.put(r)
			i += size
			continue
		}
		end := skipANSI(string(data), i)
		if end == i+1 || (end <= len(data) && end > i && data[end-1] < 0x40) {
			v.pend = append(v.pend, data[i:]...) // truncated escape
			return len(p), nil
		}
		v.csi(string(data[i:end]))
		i = end
	}
	return len(p), nil
}

func (v *vtScreen) put(r rune) {
	if v.row < 0 || v.row >= v.h || v.col < 0 || v.col >= v.w {
		return
	}
	v.cells[v.row][v.col] = vtCell{r: r, s: v.cur}
	v.col++
	for w := runewidth.RuneWidth(r); w > 1 && v.col < v.w; w-- {
		v.cells[v.row][v.col] = vtCell{r: vtWideTail, s: v.cur} // wide-glyph tail cell
		v.col++
	}
	if v.col >= v.w {
		// The pager runs with autowrap off (see transcript.enter), so the cursor
		// sticks on the last column instead of moving past it. Modelling this
		// matters: an erase-to-end-of-line issued afterwards would wipe the
		// column just written.
		v.col = v.w - 1
	}
}

// vtWideTail marks the second column of a double-width glyph.
const vtWideTail = '\u0000' + 1

func (v *vtScreen) csi(seq string) {
	if len(seq) < 3 || seq[1] != '[' {
		return
	}
	body := seq[2 : len(seq)-1]
	final := seq[len(seq)-1]
	if strings.HasPrefix(body, "?") {
		return // DECSET/DECRST (synchronized update, autowrap…) — no cell effect
	}
	nums := func() []int {
		var out []int
		for _, f := range strings.Split(body, ";") {
			n, _ := strconv.Atoi(f)
			out = append(out, n)
		}
		return out
	}
	switch final {
	case 'H':
		row, col := 1, 1
		parts := strings.Split(body, ";")
		if parts[0] != "" {
			row, _ = strconv.Atoi(parts[0])
		}
		if len(parts) > 1 && parts[1] != "" {
			col, _ = strconv.Atoi(parts[1])
		}
		v.row, v.col = row-1, col-1
	case 'K':
		n := 0
		if body != "" {
			n = nums()[0]
		}
		if v.row < 0 || v.row >= v.h {
			return
		}
		from, to := v.col, v.w
		switch n {
		case 1:
			from, to = 0, v.col+1
		case 2:
			from, to = 0, v.w
		}
		for c := from; c < to && c < v.w; c++ {
			// EL paints with the current background, not the full style.
			v.cells[v.row][c] = vtCell{r: ' ', s: vtStyle{bg: v.cur.bg}}
		}
	case 'J':
		for r := range v.cells {
			for c := range v.cells[r] {
				v.cells[r][c] = vtCell{r: ' ', s: vtStyle{bg: v.cur.bg}}
			}
		}
		v.row, v.col = 0, 0
	case 'r': // DECSTBM
		if body == "" {
			v.top, v.bot = 0, v.h-1
		} else {
			p := nums()
			v.top = p[0] - 1
			v.bot = v.h - 1
			if len(p) > 1 && p[1] > 0 {
				v.bot = p[1] - 1
			}
		}
		v.row, v.col = v.top, 0
	case 'S':
		n := 1
		if body != "" {
			n = nums()[0]
		}
		v.scroll(n)
	case 'T':
		n := 1
		if body != "" {
			n = nums()[0]
		}
		v.scroll(-n)
	case 'm':
		v.sgr(body)
	}
}

// scroll moves the scroll-region content by n rows (n>0 up, n<0 down),
// blanking what rolls in, exactly as SU/SD do.
func (v *vtScreen) scroll(n int) {
	if n == 0 || v.top < 0 || v.bot >= v.h || v.top > v.bot {
		return
	}
	blank := func(r int) {
		for c := range v.cells[r] {
			v.cells[r][c] = vtCell{r: ' ', s: vtStyle{bg: v.cur.bg}}
		}
	}
	if n > 0 {
		for r := v.top; r <= v.bot; r++ {
			if r+n <= v.bot {
				copy(v.cells[r], v.cells[r+n])
			} else {
				blank(r)
			}
		}
		return
	}
	for r := v.bot; r >= v.top; r-- {
		if r+n >= v.top {
			copy(v.cells[r], v.cells[r+n])
		} else {
			blank(r)
		}
	}
}

func (v *vtScreen) sgr(body string) {
	if body == "" {
		v.cur = vtStyle{}
		return
	}
	fields := strings.Split(body, ";")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if j := strings.IndexByte(f, ':'); j >= 0 {
			f = f[:j]
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			v.cur = vtStyle{}
		case n == 1:
			v.cur.bold = true
		case n == 2:
			v.cur.dim = true
		case n == 3:
			v.cur.italic = true
		case n == 4:
			v.cur.underline = true
		case n == 5, n == 6:
			v.cur.blink = true
		case n == 7:
			v.cur.reverse = true
		case n == 8:
			v.cur.conceal = true
		case n == 9:
			v.cur.strike = true
		case n == 21:
			v.cur.underline = true
		case n == 22:
			v.cur.bold, v.cur.dim = false, false
		case n == 23:
			v.cur.italic = false
		case n == 24:
			v.cur.underline = false
		case n == 25, n == 26:
			v.cur.blink = false
		case n == 27:
			v.cur.reverse = false
		case n == 28:
			v.cur.conceal = false
		case n == 29:
			v.cur.strike = false
		case n >= 30 && n <= 37, n >= 90 && n <= 97:
			v.cur.fg = f
		case n == 39:
			v.cur.fg = ""
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			v.cur.bg = f
		case n == 49:
			v.cur.bg = ""
		case n == 38, n == 48:
			args, consumed := vtExtendedColor(fields[i+1:])
			i += consumed
			if n == 38 {
				v.cur.fg = args
			} else {
				v.cur.bg = args
			}
		}
	}
}

func vtExtendedColor(rest []string) (string, int) {
	if len(rest) == 0 {
		return "", 0
	}
	switch rest[0] {
	case "5":
		if len(rest) >= 2 {
			return "i" + rest[1], 2
		}
	case "2":
		if len(rest) >= 4 {
			return "rgb" + strings.Join(rest[1:4], ","), 4
		}
	}
	return "", 1
}

// grid renders the appearance of every cell, for diffing two replays.
func (v *vtScreen) grid() []string {
	out := make([]string, v.h)
	for r := 0; r < v.h; r++ {
		var b strings.Builder
		for c := 0; c < v.w; c++ {
			a := v.cells[r][c].appearance()
			fmt.Fprintf(&b, "%c|%s|%s|%v%v%v%v%v%v%v;", a.r, a.s.fg, a.s.bg,
				a.s.bold, a.s.dim, a.s.italic, a.s.underline, a.s.reverse, a.s.strike, a.s.blink)
		}
		out[r] = b.String()
	}
	return out
}

// text is the human-readable form, for failure messages.
func (v *vtScreen) text() []string {
	out := make([]string, v.h)
	for r := 0; r < v.h; r++ {
		var b strings.Builder
		for c := 0; c < v.w; c++ {
			ch := v.cells[r][c].r
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		out[r] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// naivePaint is the pre-optimization painter, kept as the reference oracle.
func naivePaint(w io.Writer, screen, prev []string) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(prev) {
			old = prev[r]
		}
		if screen[r] != old {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, screen[r])
		}
	}
	b.WriteString("\x1b[?2026l")
	io.WriteString(w, b.String())
}

func assertSameGrid(t *testing.T, want, got *vtScreen, what string) {
	t.Helper()
	wg, gg := want.grid(), got.grid()
	for r := range wg {
		if wg[r] != gg[r] {
			t.Fatalf("%s: row %d differs\n reference: %q\n optimized: %q\n cells ref: %s\n cells got: %s",
				what, r, want.text()[r], got.text()[r], wg[r], gg[r])
		}
	}
}

// ---------------------------------------------------------------------------

func TestCompactRow_PreservesAppearance(t *testing.T) {
	dim := "\x1b[38;5;252m"
	rows := []string{
		"",
		"plain text with trailing spaces          ",
		dim + " " + "\x1b[0m" + dim + " " + "\x1b[0m" + dim + " " + "\x1b[0m",
		dim + "hello" + "\x1b[0m" + dim + " world  " + "\x1b[0m",
		"\x1b[7mreverse video\x1b[27m tail",
		"bg run: \x1b[41m      \x1b[0m end",
		"bg trailing: \x1b[48;5;22m      \x1b[0m",
		"bg trailing unclosed: \x1b[48;5;22m      ",
		"underline blanks: \x1b[4m    \x1b[24m|",
		"strike blanks: \x1b[9m    \x1b[29m|",
		"truecolor \x1b[38;2;10;20;30mfg\x1b[0m and \x1b[48;2;1;2;3mbg\x1b[0m   ",
		"\x1b[2m" + strings.Repeat("─", 40) + "\x1b[0m",
		"\x1b[2mnever reset, dim to the end          ",
		"many styles: \x1b[31ma\x1b[32mb\x1b[33mc\x1b[34md\x1b[35me\x1b[36mf\x1b[37mg\x1b[91mh\x1b[92mi\x1b[93mj",
		"unknown escape \x1b[3Gmoved\x1b[0m   ",
		"osc-ish \x1b]0;title\x07 tail",
		"\x1b[1;38;5;99mmulti param\x1b[0m  ",
		"\x1b[38;5;252m \x1b[0m\x1b[7m \x1b[27m\x1b[38;5;252m \x1b[0m",
		"reset-prefixed \x1b[0;31mred\x1b[0m ",
	}
	for i, row := range rows {
		want, got := newVT(80, 1), newVT(80, 1)
		naivePaint(want, []string{row}, nil)
		var buf []byte
		buf = append(buf, "\x1b[1;1H\x1b[2K"...)
		buf = compactRow(buf, row)
		got.Write(buf)
		assertSameGrid(t, want, got, fmt.Sprintf("row %d %q", i, row))
	}
}

// TestCompactRow_ShrinksRendererChurn pins the actual win: the renderers emit
// a styled escape pair per padding cell, and a full-width blank row must not
// cost 1.5 KB.
func TestCompactRow_ShrinksRendererChurn(t *testing.T) {
	row := strings.Repeat("\x1b[38;5;252m \x1b[0m", 96)
	got := string(compactRow(nil, row))
	if len(got) != 0 {
		t.Fatalf("blank padded row compacted to %d bytes (%q), want 0", len(got), got)
	}
	styled := "\x1b[38;5;252mhi\x1b[0m" + strings.Repeat("\x1b[38;5;252m \x1b[0m", 90)
	got = string(compactRow(nil, styled))
	if len(got) > 24 {
		t.Fatalf("styled row + padding compacted to %d bytes (%q), want <= 24", len(got), got)
	}
}

// TestTranscriptPaint_MatchesNaiveRepaint is the end-to-end guarantee: over a
// long scroll of a real transcript, the optimized escape stream and the naive
// one leave identical screens.
func TestTranscriptPaint_MatchesNaiveRepaint(t *testing.T) {
	const w, h = 100, 24
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, 12)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, 12)}
	}
	client.Apply(aria.AriaRead{Committed: committed})

	got := newVT(w, h)
	tr := newTranscript(got, w, h, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()

	want := newVT(w, h)
	var wantPrev []string

	step := func(what string) {
		t.Helper()
		naivePaint(want, tr.prev, wantPrev)
		wantPrev = tr.prev
		assertSameGrid(t, want, got, what)
	}
	step("enter")

	for i := range 60 {
		tr.scrollBy(-1)
		step(fmt.Sprintf("scroll up %d", i))
	}
	for i := range 40 {
		tr.scrollBy(3)
		step(fmt.Sprintf("scroll down %d", i))
	}
	tr.matchQuery = "transcript" // reverse-video highlights on every match
	tr.render()
	step("search highlight")
	for i := range 20 {
		tr.scrollBy(-2)
		step(fmt.Sprintf("highlighted scroll %d", i))
	}
	tr.showHelp = true
	tr.render()
	step("help panel")
	tr.key('G')
	step("follow tail")
}

func BenchmarkCompactRow(b *testing.B) {
	row := "\x1b[38;5;252mParagraph 0 of message 19. The quick brown fox jumps over the lazy dog\x1b[0m" +
		strings.Repeat("\x1b[38;5;252m \x1b[0m", 20)
	b.ReportAllocs()
	b.SetBytes(int64(len(row)))
	buf := make([]byte, 0, 512)
	b.ResetTimer()
	for range b.N {
		buf = compactRow(buf[:0], row)
	}
}

// ---------------------------------------------------------------------------
// Shifted-frame (scroll region) painting.
// ---------------------------------------------------------------------------

// teeVT records the raw escape stream while replaying it, so a test can assert
// both what was emitted and what it drew.
type teeVT struct {
	*vtScreen
	raw []byte
}

func newTeeVT(w, h int) *teeVT { return &teeVT{vtScreen: newVT(w, h)} }

func (t *teeVT) Write(p []byte) (int, error) {
	t.raw = append(t.raw, p...)
	return t.vtScreen.Write(p)
}

func (t *teeVT) lastFrame() string {
	s := string(t.raw)
	if i := strings.LastIndex(s, "\x1b[?2026h"); i >= 0 {
		return s[i:]
	}
	return s
}

func (t *teeVT) reset() { t.raw = nil }

func scrollTranscript(tb testing.TB, out io.Writer, w, h, messages int) *transcript {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, messages)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, 30)}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	tr := newTranscript(out, w, h, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()
	return tr
}

// TestTranscriptPaint_UsesScrollRegion pins the mechanism: a one-line scroll
// must move the rows rather than retransmit them.
func TestTranscriptPaint_UsesScrollRegion(t *testing.T) {
	out := newTeeVT(100, 40)
	tr := scrollTranscript(t, out, 100, 40, 12)
	tr.scrollBy(-40)
	out.reset()
	tr.scrollBy(-1)

	frame := out.lastFrame()
	if !strings.Contains(frame, "\x1b[1;37r") {
		t.Fatalf("no scroll region in frame: %q", frame)
	}
	if !strings.Contains(frame, "\x1b[1T") {
		t.Fatalf("no scroll-down in frame: %q", frame)
	}
	if len(frame) > 1200 {
		t.Fatalf("one-line scroll emitted %d bytes; the point is that it should not", len(frame))
	}

	out.reset()
	tr.scrollBy(1)
	if frame = out.lastFrame(); !strings.Contains(frame, "\x1b[1S") {
		t.Fatalf("scrolling the other way must scroll up: %q", frame)
	}
}

// TestTranscriptPaint_ScrollRegionOptOut proves the escape hatch really falls
// back to the full-frame diff, and that the fallback still draws correctly.
func TestTranscriptPaint_ScrollRegionOptOut(t *testing.T) {
	defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
	transcriptScrollRegions = false

	out := newTeeVT(100, 40)
	tr := scrollTranscript(t, out, 100, 40, 12)
	tr.scrollBy(-40)

	want := newVT(100, 40)
	var wantPrev []string
	naivePaint(want, tr.prev, wantPrev)
	wantPrev = tr.prev
	out.reset()
	tr.scrollBy(-1)
	if strings.Contains(out.lastFrame(), "r\x1b[") {
		t.Fatalf("opt-out still emitted a scroll region: %q", out.lastFrame())
	}
	naivePaint(want, tr.prev, wantPrev)
	assertSameGrid(t, want, out.vtScreen, "opt-out fallback")
}

// TestTranscriptPaint_ScrollWithLiveContent is the case a naive "shift by the
// offset delta" implementation gets wrong: the viewport moves AND the content
// under it changes in the same frame. That is the ordinary streaming frame —
// following the tail while a message grows scrolls the body up by however many
// rows the new text wrapped to, and the last row is new. Detection is from the
// frames themselves and the diff runs against the predicted post-scroll grid,
// so the changed rows are still repainted.
func TestTranscriptPaint_ScrollWithLiveContent(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, 8)
	for i := range committed {
		committed[i] = aria.Committed{LT: i + 1, Role: "assistant", Nodes: heavyNodes(i+1, 10)}
	}
	client.Apply(aria.AriaRead{Committed: committed})

	got := newVT(100, 30)
	tr := newTranscript(got, 100, 30, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()
	want := newVT(100, 30)
	var wantPrev []string
	check := func(what string) {
		t.Helper()
		naivePaint(want, tr.prev, wantPrev)
		wantPrev = tr.prev
		assertSameGrid(t, want, got, what)
	}
	check("enter")

	grow := func(i int) {
		client.Apply(aria.AriaRead{Live: &aria.Live{
			LT: 100, V: i + 1, Role: "assistant",
			Nodes: []aria.NodeDelta{{ID: "n1", Set: map[string]any{
				"type":     string(livedoc.NodeProse),
				"markdown": strings.Repeat(fmt.Sprintf("streaming chunk %d. ", i), i+1),
			}}},
		}})
	}
	for i := range 30 {
		grow(i)
		tr.render()
		check(fmt.Sprintf("streaming tail %d", i))
	}
	if !strings.Contains(strings.Join(tr.prev, "\n"), "streaming chunk") {
		t.Fatal("fixture never rendered the live message; the test would prove nothing")
	}
	// Now leave follow mode mid-stream and scroll against a frozen open
	// message: viewport motion and content motion decoupled.
	for i := range 20 {
		grow(30 + i)
		tr.scrollBy(-1)
		check(fmt.Sprintf("held-open scroll %d", i))
	}
	for i := range 20 {
		grow(50 + i)
		tr.scrollBy(2)
		check(fmt.Sprintf("held-open scroll down %d", i))
	}
}

// TestTranscriptPaint_ShiftedFrameProperty drives paint directly with
// synthetic frames — pure shifts, shifts with edits, unrelated screens — and
// asserts the optimized stream and the naive full-repaint stream always agree.
// This is the safety net for the shift detector's heuristics.
func TestTranscriptPaint_ShiftedFrameProperty(t *testing.T) {
	const w, h = 60, 20
	rnd := rand.New(rand.NewSource(20260724))
	styles := []string{"", "\x1b[2m", "\x1b[38;5;252m", "\x1b[1;31m", "\x1b[7m", "\x1b[48;5;22m", "\x1b[4m"}
	makeRow := func() string {
		if rnd.Intn(8) == 0 {
			return ""
		}
		st := styles[rnd.Intn(len(styles))]
		body := fmt.Sprintf("row-%06d %s", rnd.Intn(1000000), strings.Repeat("x", rnd.Intn(30)))
		pad := strings.Repeat(" ", rnd.Intn(12))
		if st == "" {
			return body + pad
		}
		return st + body + pad + "\x1b[0m"
	}

	got := newVT(w, h)
	tr := &transcript{out: got, w: w, h: h, active: true}
	want := newVT(w, h)
	var wantPrev []string

	screen := make([]string, h)
	for r := range screen {
		screen[r] = makeRow()
	}
	for step := range 400 {
		next := make([]string, h)
		switch rnd.Intn(6) {
		case 0: // unrelated screen
			for r := range next {
				next[r] = makeRow()
			}
		case 1: // a few local edits
			copy(next, screen)
			for range rnd.Intn(4) + 1 {
				next[rnd.Intn(h)] = makeRow()
			}
		case 4: // tail edits: rows sharing a long prefix (the suffix-update path)
			copy(next, screen)
			for range rnd.Intn(3) + 1 {
				r := rnd.Intn(h)
				next[r] = tailEdit(rnd, screen[r])
			}
		default: // shift, sometimes with edits on top
			n := rnd.Intn(9) - 4
			if n == 0 {
				n = 1
			}
			for r := range next {
				if src := r + n; src >= 0 && src < h {
					next[r] = screen[src]
				} else {
					next[r] = makeRow()
				}
			}
			for range rnd.Intn(3) {
				next[rnd.Intn(h)] = makeRow()
			}
		}
		tr.paint(next)
		naivePaint(want, next, wantPrev)
		wantPrev = next
		assertSameGrid(t, want, got, fmt.Sprintf("step %d", step))
		screen = next
	}
}

// tailEdit produces a row sharing a long, well-formed prefix with a template —
// the shape of the footer rule (a hundred dashes plus a changing counter) and of
// a streaming line of prose. Rows always close their style: the property oracle
// is the literal old painter, whose erase-line inherits a leaked background
// (see TestTranscriptPaint_NoBackgroundBleed), so comparing against it is only
// meaningful for rows the renderers could actually emit.
func tailEdit(rnd *rand.Rand, row string) string {
	if len(row)%2 == 0 {
		return "\x1b[2m" + strings.Repeat("\u2500", 30) + " " + fmt.Sprint(rnd.Intn(100000)) + "\x1b[0m"
	}
	return strings.Repeat("prefix ", 6) + fmt.Sprint(rnd.Intn(100000))
}

// TestTranscriptPaint_UsesSuffixUpdate pins the mechanism on the row that
// motivated it: the footer rule, a hundred columns of box drawing whose only
// changing part is the position counter at the right margin.
func TestTranscriptPaint_UsesSuffixUpdate(t *testing.T) {
	out := newTeeVT(100, 40)
	tr := scrollTranscript(t, out, 100, 40, 12)
	tr.scrollBy(-40)
	out.reset()
	tr.scrollBy(-1)

	frame := out.lastFrame()
	if !strings.Contains(frame, ";1H") && !regexpColumnAddress.MatchString(frame) {
		t.Fatalf("expected a column-addressed update in %q", frame)
	}
	if !regexpColumnAddress.MatchString(frame) {
		t.Fatalf("footer rule was retransmitted whole instead of updated at its tail: %q", frame)
	}
	if len(frame) > 400 {
		t.Fatalf("one-line scroll emitted %d bytes, want well under 400", len(frame))
	}
}

var regexpColumnAddress = regexp.MustCompile(`\x1b\[[0-9]+;[2-9][0-9]*H`)

// TestCommonRowPrefix_Guards: the suffix path must decline anything whose
// column count it cannot be sure of, because a miscount is corruption rather
// than waste.
func TestCommonRowPrefix_Guards(t *testing.T) {
	long := strings.Repeat("\u2500", 40)
	cases := []struct {
		name     string
		old, new string
		want     bool
	}{
		{"long ascii prefix", strings.Repeat("a", 40) + "xx", strings.Repeat("a", 40) + "yy", true},
		{"long box prefix", long + " 1/9", long + " 2/9", true},
		{"styled prefix", "\x1b[2m" + long + " 1/9\x1b[0m", "\x1b[2m" + long + " 2/9\x1b[0m", true},
		{"short prefix", "abc1", "abc2", false},
		{"wide glyph", strings.Repeat("\u4e16", 40) + "a", strings.Repeat("\u4e16", 40) + "b", false},
		{"combining mark", strings.Repeat("e\u0301", 40) + "a", strings.Repeat("e\u0301", 40) + "b", false},
		{"non-sgr escape", strings.Repeat("a", 20) + "\x1b[3G" + strings.Repeat("a", 20) + "x",
			strings.Repeat("a", 20) + "\x1b[3G" + strings.Repeat("a", 20) + "y", false},
		{"identical", long, long, false},
	}
	for _, c := range cases {
		_, col, _, ok := commonRowPrefix(c.old, c.new)
		if ok != c.want {
			t.Errorf("%s: commonRowPrefix ok = %v (col %d), want %v", c.name, ok, col, c.want)
		}
	}
	if _, col, _, _ := commonRowPrefix(long+" 1/9", long+" 2/9"); col != 41 {
		t.Errorf("box-drawing prefix measured %d columns, want 41", col)
	}
}

// TestTranscriptPaint_WideGlyphsFallBack replays rows full of double-width and
// combining characters: the guards must keep the screen correct.
func TestTranscriptPaint_WideGlyphsFallBack(t *testing.T) {
	const w, h = 40, 8
	got := newVT(w, h)
	tr := &transcript{out: got, w: w, h: h, active: true}
	want := newVT(w, h)
	var prev []string
	rows := [][]string{
		{"\u4e16\u754c\u4e16\u754c\u4e16\u754c\u4e16\u754c 1", "e\u0301e\u0301e\u0301 a", "plain", "", "", "", "", ""},
		{"\u4e16\u754c\u4e16\u754c\u4e16\u754c\u4e16\u754c 2", "e\u0301e\u0301e\u0301 b", "plain", "", "", "", "", ""},
		{"\u4e16\u754c 3", "e\u0301 c", "plainer", "", "", "", "", ""},
	}
	for i, screen := range rows {
		tr.paint(screen)
		naivePaint(want, screen, prev)
		prev = screen
		assertSameGrid(t, want, got, fmt.Sprintf("wide row set %d", i))
	}
}

// TestTranscriptPaint_NoBackgroundBleed pins a behaviour change that is a fix.
// The old painter emitted CSI 2K with whatever SGR the previous row left
// active, so a row that set a background and never reset it painted the NEXT
// row's erase in that colour. compactRow's "every row ends in default SGR"
// invariant removes the hazard.
func TestTranscriptPaint_NoBackgroundBleed(t *testing.T) {
	const w, h = 20, 3
	got := newVT(w, h)
	tr := &transcript{out: got, w: w, h: h, active: true}
	tr.paint([]string{"\x1b[41mred, never reset", "second row", ""})
	for c := range w {
		cell := got.cells[1][c].appearance()
		if cell.s.bg != "" {
			t.Fatalf("row 1 column %d inherited background %q from the row above", c, cell.s.bg)
		}
	}
	// And the row that did set it still shows it where it painted.
	if got.cells[0][0].s.bg != "41" {
		t.Fatalf("row 0 lost its own background: %+v", got.cells[0][0])
	}
}

// ---------------------------------------------------------------------------
// C's painter oracles, carried forward.
//
// C asserted byte-identity with paintReference (the pre-optimization painter)
// because C's change to paint() was pure buffer recycling: same escapes, fewer
// allocations. B's painter deliberately emits *different* bytes — compacted
// SGR, suffix updates, scroll regions — so byte-identity is no longer the
// intent and the assertion is re-stated at the level that still holds: the
// cells. paintReference is dropped as a duplicate of naivePaint.
//
// The frame sequence is C's, verbatim, because it was chosen to catch the
// aliasing bug the buffer recycling could introduce (frame N-1's buffer being
// handed back while frame N still refers to it), and B's painter inherits that
// hazard along with the recycling.
// ---------------------------------------------------------------------------

func TestPaintMatchesReferenceAtTheCellLevel(t *testing.T) {
	frames := [][]string{
		{"one", "two", "three"},
		{"one", "CHANGED", "three"},
		{"one", "CHANGED", "three"},
		{"", "", ""},
		{"\x1b[2mdim\x1b[0m", "日本語", ""},
		{"\x1b[2mdim\x1b[0m", "日本語", "tail"},
		{strings.Repeat("x", 200), "", "\x1b[7mhl\x1b[27m"},
	}
	const w, h = 200, 3
	got, want := newVT(w, h), newVT(w, h)
	tr := &transcript{out: got, active: true, h: h}
	var prev []string
	for i, f := range frames {
		screen := append([]string(nil), f...)
		// paint() takes ownership of the slice it is handed (it becomes t.prev),
		// so the reference gets its own copy.
		tr.paint(append([]string(nil), f...))
		naivePaint(want, screen, prev)
		prev = screen
		assertSameGrid(t, want, got, fmt.Sprintf("frame %d", i))
	}
}

// TestPaintReusesBuffers pins the recycling: a steady stream of frames must
// not allocate a new output buffer or frame slice every time. Unchanged from
// C except for the screen height, which is now above minScrollRun*2 so the
// scroll-region planner actually runs and its scratch buffers (predBuf,
// keysNew, keysOld) are held to the same zero-allocation standard.
func TestPaintReusesBuffers(t *testing.T) {
	tr := &transcript{out: io.Discard, active: true, h: 4}
	frames := [][]string{
		{"a", "b", "c", "d"},
		{"a", "b", "c", "e"},
	}
	i := 0
	got := testing.AllocsPerRun(200, func() {
		screen := tr.nextScreen()
		copy(screen, frames[i%2])
		tr.paint(screen)
		i++
	})
	if got > 0 {
		t.Errorf("steady-state paint allocated %v times per frame, want 0", got)
	}
}

// TestPaintReusesBuffersUnderScroll is the same oracle on a screen tall enough
// for planScroll to engage on every frame.
func TestPaintReusesBuffersUnderScroll(t *testing.T) {
	const h = 24
	tr := &transcript{out: io.Discard, active: true, h: h}
	rows := make([]string, h+2)
	for i := range rows {
		rows[i] = fmt.Sprintf("\x1b[38;5;252mrow %d of a scrolling viewport\x1b[0m", i)
	}
	i := 0
	got := testing.AllocsPerRun(200, func() {
		screen := tr.nextScreen()
		copy(screen, rows[i%2:])
		tr.paint(screen)
		i++
	})
	if got > 0 {
		t.Errorf("steady-state scrolling paint allocated %v times per frame, want 0", got)
	}
}
