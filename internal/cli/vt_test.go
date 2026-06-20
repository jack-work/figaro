package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
)

// vt is a small terminal model: a grid of display cells with a cursor. It
// honors the escape vocabulary the painter emits AND models the terminal's
// auto-margin (DECAWM) so a test can see whether full-width rows wrap and
// desync the painter's one-row-per-line cursor math.
type vt struct {
	lines    [][]rune
	row, col int
	width    int
	autowrap bool // DECAWM; the live session disables it
	pending  bool // deferred-wrap latch (cursor sat at the right margin)
}

func newVT(width int, autowrap bool) *vt { return &vt{width: width, autowrap: autowrap} }

var vtCSI = regexp.MustCompile(`^\x1b\[\??([0-9;]*)([A-Za-z])`)

func (v *vt) ensure(r int) {
	for len(v.lines) <= r {
		v.lines = append(v.lines, nil)
	}
}
func (v *vt) putRune(r rune) {
	if v.pending && v.autowrap { // resolve a deferred wrap before printing
		v.row++
		v.col = 0
		v.pending = false
	}
	v.ensure(v.row)
	for len(v.lines[v.row]) <= v.col {
		v.lines[v.row] = append(v.lines[v.row], ' ')
	}
	v.lines[v.row][v.col] = r
	v.col += runewidth.RuneWidth(r)
	if v.col >= v.width {
		if v.autowrap {
			v.pending = true // sit at the margin; wrap on next printable
		} else {
			v.col = v.width - 1 // clamp: further runes overwrite the last cell
		}
	}
}
func (v *vt) feed(s string) {
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' {
			m := vtCSI.FindStringSubmatch(string(rs[i:]))
			if m == nil {
				i++
				continue
			}
			n := 0
			fmt.Sscanf(m[1], "%d", &n)
			switch m[2] {
			case "A":
				v.row -= n
				if v.row < 0 {
					v.row = 0
				}
				v.pending = false
			case "B":
				v.row += n
				v.ensure(v.row)
				v.pending = false
			case "K": // erase line (2K)
				v.ensure(v.row)
				v.lines[v.row] = nil
				v.col = 0
				v.pending = false
			case "J": // erase from cursor to end of screen
				v.ensure(v.row)
				if v.col < len(v.lines[v.row]) {
					v.lines[v.row] = v.lines[v.row][:v.col]
				}
				v.lines = v.lines[:v.row+1]
				v.pending = false
			case "l":
				v.autowrap = false // \x1b[?7l
			case "h":
				v.autowrap = true // \x1b[?7h
			}
			i += len([]rune(m[0]))
			continue
		}
		switch rs[i] {
		case '\r':
			v.col = 0
			v.pending = false
		case '\n':
			v.row++
			v.col = 0
			v.pending = false
			v.ensure(v.row)
		default:
			v.putRune(rs[i])
		}
		i++
	}
}
func (v *vt) screen() []string {
	out := make([]string, 0, len(v.lines))
	for _, l := range v.lines {
		out = append(out, strings.TrimRight(string(l), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// drive runs a node-list progression through the painter (DiffNodes → ops,
// with a spinner tick between ops) into a vt, then commits. When session is
// true it brackets the painting with the same auto-wrap toggle the live CLI
// session uses.
func drive(width int, autowrap, session bool, states [][]livedoc.Node) []string {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, width, 10)
	v := newVT(width, autowrap)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	if session {
		fmt.Fprint(&buf, autowrapOff)
	}
	lr.snapshot(nil)
	flush()
	var prev []livedoc.Node
	for _, st := range states {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
			lr.tickSpin()
			flush()
		}
		prev = st
	}
	lr.commit()
	if session {
		fmt.Fprint(&buf, autowrapOn)
	}
	flush()
	return v.screen()
}

func streamingStates() [][]livedoc.Node {
	tool := func(name, status, out string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeTool, Name: name, Status: status, Output: out}
	}
	pr := func(md string) livedoc.Node { return livedoc.Node{Type: livedoc.NodeProse, Markdown: md} }
	ls := "cmd\nduck\nflake.lock\nflake.nix\ngo.mod\ngo.sum\ninternal\nissues.md\nREADME.md\nweb\nagents.md"
	tail := "Quattro tagli, quattro specchi — ecco fatto. Branch feat/live-render, clean tree, module github.com/jack-work/figaro, and the clock confirms 4PM EDT. Ready for the real work whenever you are."
	head := pr("Ecco, quattro specchi in una volta!")
	t1 := tool("bash", "ok", ls)
	t2 := tool("bash", "ok", "feat/live-render")
	return [][]livedoc.Node{
		{pr("Ecco")},
		{head},
		{head, tool("bash", "running", "")},
		{head, tool("bash", "running", ls)},
		{head, t1},
		{head, t1, tool("bash", "running", "")},
		{head, t1, t2},
		{head, t1, t2, pr("Quattro")},
		{head, t1, t2, pr(tail[:90])},
		{head, t1, t2, pr(tail[:140])},
		{head, t1, t2, pr(tail)},
	}
}

func expectedScreen(width int, states [][]livedoc.Node) []string {
	rows, _ := renderNodes(states[len(states)-1], width, 10, 0)
	var want []string
	for _, r := range rows {
		want = append(want, liveStrip(strings.TrimRight(r, " ")))
	}
	return want
}

// With the live session's auto-wrap disabled, the painter renders the
// streamed turn with no duplication, even though glamour rows can reach the
// viewport width.
func TestVT_LiveSessionNoDuplication(t *testing.T) {
	const W = 70
	states := streamingStates()
	got := drive(W, true /*autowrap default*/, true /*session brackets*/, states)
	want := expectedScreen(W, states)
	if len(got) != len(want) {
		t.Fatalf("row count: screen=%d want=%d (duplication/loss)\n--- screen ---\n%s\n--- want ---\n%s",
			len(got), len(want), strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Right-trim both: glamour pads rows to width with spaces, and auto-wrap
	// off clips at most the last (padding) column — neither is the bug.
	for i := range want {
		g := strings.TrimRight(got[i], " ")
		w := strings.TrimRight(want[i], " ")
		if g != w {
			t.Errorf("row %d:\n got %q\nwant %q", i, g, w)
		}
	}
}

// The painter's one-row-per-line invariant rests on renderNodes never
// emitting a row wider than the viewport (a wide row wraps and desyncs the
// cursor math). glamour's margin pushes rows past the requested width, so
// clipToWidth must bring them back. This fails if that clip regresses.
func TestRenderNodes_RowsFitWidth(t *testing.T) {
	states := streamingStates()
	for _, W := range []int{30, 40, 70, 100, 160} {
		for si, st := range states {
			rows, _ := renderNodes(st, W, 10, 3)
			for ri, r := range rows {
				if vis := runewidth.StringWidth(liveStrip(r)); vis > W {
					t.Errorf("width=%d state=%d row=%d is %d cols (> width): %q", W, si, ri, vis, liveStrip(r))
				}
			}
		}
	}
}
