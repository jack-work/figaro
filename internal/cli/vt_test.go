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
	height   int  // viewport rows; 0 = unbounded (no scrolling)
	top      int  // absolute index of the viewport's first line
	autowrap bool // DECAWM; the live session disables it
	pending  bool // deferred-wrap latch (cursor sat at the right margin)
}

func newVT(width int, autowrap bool) *vt { return &vt{width: width, autowrap: autowrap} }

// newVTH is a viewport of fixed height that scrolls — newlines past the
// bottom push the viewport down, while CUD/CUU clamp at the viewport edges
// (real terminal behavior). This is what exposes scroll-related cursor
// desyncs the unbounded model can't see.
func newVTH(width, height int, autowrap bool) *vt {
	return &vt{width: width, height: height, autowrap: autowrap}
}

// scrollIfNeeded keeps the cursor inside the viewport by advancing top.
func (v *vt) scrollIfNeeded() {
	if v.height > 0 && v.row > v.top+v.height-1 {
		v.top = v.row - (v.height - 1)
	}
}

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
		v.scrollIfNeeded()
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
			case "A": // cursor up — clamps at the viewport top
				v.row -= n
				lo := v.top
				if v.row < lo {
					v.row = lo
				}
				if v.row < 0 {
					v.row = 0
				}
				v.pending = false
			case "B": // cursor down — clamps at the viewport bottom (no scroll)
				v.row += n
				if v.height > 0 && v.row > v.top+v.height-1 {
					v.row = v.top + v.height - 1
				}
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
			v.scrollIfNeeded()
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
	rows, _ := renderNodes(states[len(states)-1], width, 10, 0, renderSettings{})
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

// A prose code block streamed token-by-token (the fence open for many
// frames before it closes) must converge to the same render as the final
// state — the synth-closed mid-stream code block keeps the structure
// stable, so there's no restructure churn when "```" finally arrives.
func TestVT_StreamingCodeBlockConverges(t *testing.T) {
	const W = 72
	full := "Final layout:\n\n```\ngym/\n├── bootstrap/\n│   └── main.tf\n└── infra/live/dev/\n```\n\nThe S3 backend has the real state."
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, W, 10)
	v := newVT(W, true)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	fmt.Fprint(&buf, autowrapOff)
	lr.snapshot(nil)
	var prev []livedoc.Node
	for n := 4; n < len(full); n += 11 { // grow in chunks, fence open for a while
		st := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: full[:n]}}
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
		}
		prev = st
		flush()
	}
	st := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: full}}
	for _, op := range livedoc.DiffNodes(prev, st) {
		lr.applyOp(op)
	}
	lr.commit()
	fmt.Fprint(&buf, autowrapOn)
	flush()

	got := v.screen()
	want := expectedScreen(W, [][]livedoc.Node{st})
	if len(got) != len(want) {
		t.Fatalf("streamed code block didn't converge: screen=%d want=%d rows\n--- screen ---\n%s",
			len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if g, w := strings.TrimRight(got[i], " "), strings.TrimRight(want[i], " "); g != w {
			t.Errorf("row %d:\n got %q\nwant %q", i, g, w)
		}
	}
}

// When the conversation has scrolled (the turn renders near the viewport
// bottom), committing a multi-row unit must scroll, not CursorDown — CUD
// clamps at the bottom and the next unit lands on top of the last row.
// This drives a unit boundary at the bottom of a short viewport and checks
// the prompt's wrapped 2nd line survives. (Reverting commit() to
// CursorDown makes this fail.)
func TestVT_ScrollUnitBoundaryNoClobber(t *testing.T) {
	const W, H = 80, 8
	pr := func(s string) livedoc.Node { return livedoc.Node{Type: livedoc.NodeProse, Markdown: s} }
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, W, 10)
	v := newVTH(W, H, true)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	v.feed(strings.Repeat("filler\n", H)) // push the cursor to the viewport bottom

	buf.WriteString(autowrapOff)
	lr.snapshot([]livedoc.Node{pr("Reply in two short paragraphs: first greet me warmly in one sentence, then restate exactly what I asked.")})
	lr.commit()
	flush()
	lr.snapshot(nil)
	var prev []livedoc.Node
	for _, st := range [][]livedoc.Node{
		{pr("Buonasera, maestro!")},
		{pr("Buonasera, maestro!"), pr("You asked me to greet and restate.")},
	} {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
		}
		prev = st
	}
	lr.commit()
	flush()

	all := strings.Join(v.screen(), "\n")
	if !strings.Contains(all, "restate exactly what I asked.") {
		t.Fatalf("prompt's wrapped 2nd line was clobbered at the unit boundary under scroll:\n%s", all)
	}
}

// driveH runs a node-list progression through the painter into a FINITE
// scrolling viewport (newVTH) with a pinned bookend, then commits. It mimics
// the live CLI session brackets (auto-wrap off) and feeds after every op.
func driveH(width, height int, bookend func() string, states [][]livedoc.Node) []string {
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, width, 10)
	lr.height = height
	lr.bookend = bookend
	v := newVTH(width, height, true)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	fmt.Fprint(&buf, autowrapOff)
	lr.snapshot(nil)
	flush()
	var prev []livedoc.Node
	for _, st := range states {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
		}
		prev = st
	}
	lr.commit()
	fmt.Fprint(&buf, autowrapOn)
	flush()
	return v.screen()
}

// A long multi-paragraph prose node, streamed in growing chunks (glamour
// reflows the whole prose every delta), with a pinned bookend, where the live
// tail (prose + bookend) grows TALLER than the viewport. The painter must not
// duplicate content lines, must not leave a run of trailing blank lines before
// the bookend, and the bookend must remain the last visible line.
func TestVT_LiveTailExceedsViewportNoDup(t *testing.T) {
	const W, H = 70, 10
	body := "First paragraph that wraps across a couple of terminal lines so the prose has some height to it.\n\n" +
		"Second paragraph, also long enough to wrap, adding more rows so the rendered prose plus the bookend overflows the short viewport.\n\n" +
		"Third paragraph closes things out, e si sente la cucina canta. Quattro tagli, quattro specchi — ecco fatto and the live tail is now well past the viewport height."
	var states [][]livedoc.Node
	for n := 20; n < len(body); n += 23 {
		states = append(states, []livedoc.Node{prose(body[:n])})
	}
	states = append(states, []livedoc.Node{prose(body)})

	got := driveH(W, H, func() string { return "BOOKEND" }, states)

	all := strings.Join(got, "\n")
	if len(got) == 0 || got[len(got)-1] != "BOOKEND" {
		t.Fatalf("bookend is not the last visible line:\n%s", all)
	}
	// At most the single by-design blank separator before the bookend — not
	// a run of trailing blanks (the whitespace-occlusion symptom).
	blanks := 0
	for i := len(got) - 2; i >= 0 && strings.TrimSpace(got[i]) == ""; i-- {
		blanks++
	}
	if blanks > 1 {
		t.Fatalf("%d blank line(s) between content and bookend (expected ≤1):\n%s", blanks, all)
	}
	// No duplicated content lines (the duplication symptom). Compare the
	// non-blank, non-bookend visible rows to the canonical final render.
	want := map[string]int{}
	finalRows, _ := renderNodes(states[len(states)-1], W, 10, 0, renderSettings{})
	for _, r := range finalRows {
		if s := strings.TrimRight(liveStrip(r), " "); s != "" {
			want[s]++
		}
	}
	seen := map[string]int{}
	for _, r := range got {
		s := strings.TrimRight(liveStrip(r), " ")
		if s == "" || s == "BOOKEND" {
			continue
		}
		seen[s]++
		if seen[s] > want[s] {
			t.Fatalf("duplicated content line %q (seen %d, want %d):\n%s", s, seen[s], want[s], all)
		}
	}
}

// bashTool builds a bash tool node with a long command arg so that toggling
// verbose changes its rendered height (collapsed: 1 header row; verbose: the
// full wrapped command beneath the header).
func bashTool(status, cmd, out string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: status,
		Args: map[string]any{"command": cmd}, Output: out}
}

// Toggling verbosity (Ctrl-O) after a tool node has flushed to scrollback must
// NOT corrupt the live region: the flushed tool stays frozen (collapsed) and
// the live prose below it is neither duplicated nor erased. Pre-fix this
// duplicated the flushed tool's rows because the watermark was a row count and
// the toggle changed the flushed nodes' rendered height. Unbounded vt.
func TestVT_VerbosityToggleAfterFlushNoCorruption(t *testing.T) {
	const W = 70
	longCmd := "grep -rn 'some quite long pattern that wraps across the terminal width' internal/cli/*.go | sort -u"
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, W, 10)
	v := newVT(W, false)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	fmt.Fprint(&buf, autowrapOff)
	lr.snapshot(nil)

	states := [][]livedoc.Node{
		{bashTool(livedoc.StatusRunning, longCmd, "")},
		{bashTool(livedoc.StatusOK, longCmd, "done")},
		{bashTool(livedoc.StatusOK, longCmd, "done"), prose("All set, the command finished cleanly.")},
	}
	var prev []livedoc.Node
	for _, st := range states {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
		}
		prev = st
	}
	lr.setSettings(renderSettings{verbose: true})
	flush()

	assertNoDupAndProse(t, v.screen(), W, states[len(states)-1])
}

// Same toggle bug under a finite scrolling viewport, where a desync also
// erases a line.
func TestVT_VerbosityToggleAfterFlushNoCorruptionViewport(t *testing.T) {
	const W, H = 70, 12
	longCmd := "grep -rn 'some quite long pattern that wraps across the terminal width' internal/cli/*.go | sort -u"
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, W, 10)
	lr.height = H
	v := newVTH(W, H, false)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	fmt.Fprint(&buf, autowrapOff)
	lr.snapshot(nil)

	states := [][]livedoc.Node{
		{bashTool(livedoc.StatusRunning, longCmd, "")},
		{bashTool(livedoc.StatusOK, longCmd, "done")},
		{bashTool(livedoc.StatusOK, longCmd, "done"), prose("All set, the command finished cleanly.")},
	}
	var prev []livedoc.Node
	for _, st := range states {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
		}
		prev = st
	}
	lr.setSettings(renderSettings{verbose: true})
	flush()

	assertNoDupAndProse(t, v.screen(), W, states[len(states)-1])
}

// assertNoDupAndProse checks that after a post-flush verbosity toggle: the
// prose line is still present, no content line is duplicated beyond its
// canonical count (collapsed tool — the flushed node never expands), and the
// flushed collapsed header survives.
func assertNoDupAndProse(t *testing.T, after []string, width int, final []livedoc.Node) {
	t.Helper()
	all := strings.Join(after, "\n")
	if !strings.Contains(liveStrip(all), "All set, the command finished cleanly.") {
		t.Fatalf("prose line lost after toggle:\n%s", all)
	}
	// The flushed tool stays COLLAPSED (frozen in scrollback): the verbose
	// per-line wrapped command must NOT appear.
	wantCollapsed, _ := renderNodes(final, width, 10, 0, renderSettings{})
	want := map[string]int{}
	for _, r := range wantCollapsed {
		if s := strings.TrimRight(liveStrip(r), " "); s != "" {
			want[s]++
		}
	}
	seen := map[string]int{}
	for _, r := range after {
		s := strings.TrimRight(liveStrip(r), " ")
		if s == "" {
			continue
		}
		seen[s]++
		if seen[s] > want[s] {
			t.Fatalf("duplicated/expanded content line %q (seen %d, want %d) after toggle:\n%s",
				s, seen[s], want[s], all)
		}
	}
}

// Toggling verbose while a tool is still LIVE (not yet flushed) DOES expand it
// — the toggle still takes effect on live content.
func TestVT_VerbosityToggleWhileLiveExpands(t *testing.T) {
	const W = 70
	longCmd := "grep -rn 'some quite long pattern that wraps across the terminal width' internal/cli/*.go | sort -u"
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, W, 10)
	v := newVT(W, false)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	fmt.Fprint(&buf, autowrapOff)
	lr.snapshot(nil)

	st := []livedoc.Node{bashTool(livedoc.StatusRunning, longCmd, "out")}
	for _, op := range livedoc.DiffNodes(nil, st) {
		lr.applyOp(op)
		flush()
	}
	collapsedRows := len(v.screen())

	lr.setSettings(renderSettings{verbose: true})
	flush()
	expandedRows := len(v.screen())

	if expandedRows <= collapsedRows {
		t.Fatalf("live tool did not expand on verbose toggle: collapsed=%d expanded=%d",
			collapsedRows, expandedRows)
	}
	// The wrapped command lines should now be on screen.
	wantRows, _ := renderNodes(st, W, 10, lr.tick, renderSettings{verbose: true})
	got := strings.Join(v.screen(), "\n")
	for _, r := range wantRows {
		s := strings.TrimRight(liveStrip(r), " ")
		if s == "" {
			continue
		}
		if !strings.Contains(liveStrip(got), s) {
			t.Fatalf("verbose row %q missing after live toggle:\n%s", s, got)
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
			rows, _ := renderNodes(st, W, 10, 3, renderSettings{})
			for ri, r := range rows {
				if vis := runewidth.StringWidth(liveStrip(r)); vis > W {
					t.Errorf("width=%d state=%d row=%d is %d cols (> width): %q", W, si, ri, vis, liveStrip(r))
				}
			}
		}
	}
}

// A tool whose command spans multiple lines must never emit a row with an
// embedded newline (or other control char): a multi-physical-line row
// desyncs the painter's one-row-per-line cursor math and duplicates output.
func TestRenderNodes_NoEmbeddedControlChars(t *testing.T) {
	cmd := "aws iam attach-user-policy \\\n  --user-name terraform \\\n  --policy-arn arn:aws:iam::aws:policy/AdministratorAccess"
	tool := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: "ok",
		Args: map[string]interface{}{"command": cmd}, Output: "ok\ndone"}
	for _, verbose := range []bool{false, true} {
		for _, W := range []int{40, 80, 160} {
			rows, _ := renderNodes([]livedoc.Node{tool}, W, 10, 0, renderSettings{verbose: verbose})
			for ri, r := range rows {
				for _, c := range liveStrip(r) {
					if c < 0x20 || c == 0x7f {
						t.Fatalf("verbose=%v W=%d row %d has control char %q: %q", verbose, W, ri, c, r)
					}
				}
			}
		}
	}
}

// The multi-line-command tool, streamed running→ok then prose, must not
// duplicate its output (the embedded-newline desync repro).
func TestVT_MultilineCommandNoDup(t *testing.T) {
	cmd := "aws iam attach-user-policy \\\n  --user-name terraform \\\n  --policy-arn arn:aws:iam::aws:policy/AdministratorAccess"
	mk := func(status string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: status,
			Args: map[string]interface{}{"command": cmd}, Output: "MARKER_OUT line"}
	}
	pr := livedoc.Node{Type: livedoc.NodeProse, Markdown: "done."}
	var buf bytes.Buffer
	lr := newLiveRegion(&buf, 70, 10)
	lr.height = 10
	v := newVTH(70, 10, true)
	flush := func() { v.feed(buf.String()); buf.Reset() }
	buf.WriteString(autowrapOff)
	lr.snapshot(nil)
	var prev []livedoc.Node
	for _, st := range [][]livedoc.Node{{mk("running")}, {mk("ok")}, {mk("ok"), pr}} {
		for _, op := range livedoc.DiffNodes(prev, st) {
			lr.applyOp(op)
			flush()
			lr.tickSpin()
			flush()
		}
		prev = st
	}
	lr.commit()
	flush()
	if n := strings.Count(liveStrip(strings.Join(v.screen(), "\n")), "MARKER_OUT"); n != 1 {
		t.Fatalf("tool output appears %d times (want 1):\n%s", n, liveStrip(strings.Join(v.screen(), "\n")))
	}
}
