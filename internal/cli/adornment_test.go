package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

// The ADORNMENT as the reader sees it, state by state, against the sketch it
// was drawn from (/var/tmp/23e03e27/delta-adornments.md). Rows are compared
// plain: the glyphs and their columns are the claim, and the colours have
// their own tests below.

const adornWidth = 72

func adornDeltas(pairs ...string) map[string]livedoc.FormDelta {
	out := map[string]livedoc.FormDelta{}
	for i := 0; i+2 < len(pairs)+1; i += 3 {
		key, prev, val := pairs[i], pairs[i+1], pairs[i+2]
		d := livedoc.FormDelta{Form: "a1", Kind: livedoc.FormBound, Event: livedoc.FormSet}
		if prev != "" {
			d.Prev = json.RawMessage(`"` + prev + `"`)
		}
		d.Value = json.RawMessage(`"` + val + `"`)
		out["a1."+key] = d
	}
	return out
}

// adornFixture is one turn wearing all three layouts: a question with a fork
// and state of its own, a settled tool with state, and a line of prose with
// state.
func adornFixture(t testing.TB) *transcript { return adornFixtureH(t, 40, 1) }

// adornFixtureH is the same turn, at a given viewport height and repeated
// `turns` times: a short viewport is what makes a scroll observable.
func adornFixtureH(t testing.TB, h, turns int) *transcript {
	t.Helper()
	deltas := adornDeltas(
		"mantra", "going to test some harness behavior", "harness behavior test",
		"datetime", "", "Saturday, September 12, 2026",
	)
	turn := aria.Turn{
		ID: 1, Sealed: true, Inquiry: "what does this say?",
		FormDeltas: adornDeltas(
			"mantra", "going to test some harness behavior", "harness behavior test",
			"datetime", "", "Saturday, September 12, 2026",
			"system.forked_from", "", "e5abf08d",
		),
		Nodes: []livedoc.Node{
			{
				Type: livedoc.NodeTool, ID: "t1", Name: "bash", Status: livedoc.StatusOK,
				Args:      map[string]any{"command": "figaro set mantra"},
				Output:    "set mantra (figaro e5abf08d) @7",
				StartedAt: 1, FinishedAt: 38,
				FormDeltas: deltas,
			},
			{Type: livedoc.NodeProse, Markdown: "a quote of my own line, sliced.", FormDeltas: deltas},
		},
	}
	client := aria.NewClient()
	for i := range turns {
		part := aria.TurnPart{Turn: turn}
		part.Turn.ID = uint64(i + 1)
		client.Apply(aria.Page{Parts: []aria.TurnPart{part}}, aria.Notify)
	}
	ft := ldrender.NewFakeTerminal(adornWidth, h)
	tr := newTranscript(ft, adornWidth, h, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	return tr
}

func adornScreen(tr *transcript) []string {
	rows := stripANSIAll(tr.lines())
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	return rows
}

// adornRowsAt is the run of rows starting at the first one containing head.
func adornRowsAt(t testing.TB, tr *transcript, head string, n int) []string {
	t.Helper()
	rows := adornScreen(tr)
	for i, r := range rows {
		if strings.Contains(r, head) {
			if i+n > len(rows) {
				n = len(rows) - i
			}
			return rows[i : i+n]
		}
	}
	t.Fatalf("no row carries %q:\n%s", head, strings.Join(rows, "\n"))
	return nil
}

func wantRows(t testing.TB, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("want %d rows, got %d:\n%s", len(want), len(got), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d:\n want %q\n  got %q\n--- all ---\n%s", i, want[i], got[i], strings.Join(got, "\n"))
		}
	}
}

// COLLAPSED: one glyph in the right gutter and nothing else. The tool wears
// it on its header, prose on the end of its text, the question on the header
// row that names the voice.
func TestAdornmentCollapsedIsOneGlyphInTheGutter(t *testing.T) {
	tr := adornFixture(t)
	rows := adornScreen(tr)
	marked := 0
	for _, r := range rows {
		if strings.HasSuffix(r, "Δ") {
			marked++
			if n := len([]rune(r)); n != adornWidth {
				t.Fatalf("the marker stands in the last column, not past it: %d columns in %q", n, r)
			}
		}
		if strings.Contains(r, "datetime") {
			t.Fatalf("a collapsed adornment draws no rows:\n%s", strings.Join(rows, "\n"))
		}
	}
	if marked != 3 {
		t.Fatalf("three adorned blocks, three markers, got %d:\n%s", marked, strings.Join(rows, "\n"))
	}
	if !strings.HasSuffix(rows[0], "Δ") || !strings.Contains(rows[0], "> input ⑂ e5abf08d") {
		t.Fatalf("the question's fork rides its header row, the marker its gutter: %q", rows[0])
	}
}

// THE FORK IS LIFTED, and lifted means gone from the list: the header says it
// once. The keys the fork patch wrote are what the header is made of.
func TestInquiryLiftsTheForkOutOfItsList(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.adorned[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 4),
		"  what does this say?",
		"Δ",
		"│ datetime  ∅ -> Saturday, September 12, 2026",
		"│ mantra    going to test some harness behavi… -> harness behavior test",
	)
	rows := adornScreen(tr)
	if strings.Contains(strings.Join(rows, "\n"), "forked_from") {
		t.Fatalf("the lifted fork left a row behind:\n%s", strings.Join(rows, "\n"))
	}
	// The enclosure closes on the rule below it.
	for _, r := range rows {
		if strings.HasPrefix(r, "╰") {
			return
		}
	}
	t.Fatalf("the question's list must close on the rule:\n%s", strings.Join(rows, "\n"))
}

// A NODE'S FORK IS A DELTA LIKE ANY OTHER. Only the question invariantly
// carries one (see forkParent), so nothing lifts anywhere else: the same set
// of deltas hides its fork under the question's coordinate and shows it under
// a node's.
func TestNodeForkIsAnOrdinaryRow(t *testing.T) {
	deltas := adornDeltas(
		"mantra", "", "harness behavior test",
		"system.forked_from", "", "e5abf08d",
	)
	if n := adornRowCount(deltas, adornLift(inquiryNode)); n != 1 {
		t.Fatalf("the question lifts its fork out of the list: %d rows", n)
	}
	if n := adornRowCount(deltas, adornLift(0)); n != 2 {
		t.Fatalf("a node lists its fork like any other key: %d rows", n)
	}
	row, ok := adornRowFull(deltas, adornLift(0), 2)
	if !ok || !strings.Contains(row, "system.forked_from") {
		t.Fatalf("a node's fork row says what it is: %q", row)
	}
}

// THE SNAKE, as sketched. Open with nothing selected the head parks at the
// anchor; with a row selected the body runs from the anchor down to it and
// stops there.
func TestToolSnakeFollowsTheSelection(t *testing.T) {
	tr := adornFixture(t)
	ref := nodeRef{turn: 1, index: 0}
	tr.adorned[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})

	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 4),
		"  │ set mantra (figaro e5abf08d) @7",
		" Δ╯",
		"   datetime  ∅ -> Saturday, September 12, 2026",
		"   mantra    going to test some harness behav… -> harness behavior test",
	)

	tr.selectRef(deltaRefOf(ref, 1), false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 4),
		"  │ set mantra (figaro e5abf08d) @7",
		" ╭╯",
		" Δ datetime  ∅ -> Saturday, September 12, 2026",
		"   mantra    going to test some harness behav… -> harness behavior test",
	)

	tr.selectRef(deltaRefOf(ref, 2), false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 4),
		"  │ set mantra (figaro e5abf08d) @7",
		" ╭╯",
		" │ datetime  ∅ -> Saturday, September 12, 2026",
		" Δ mantra    going to test some harness behav… -> harness behavior test",
	)

	// The state resets when the selection leaves the list.
	tr.selectRef(nodeRef{turn: 1, index: 1}, false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 2),
		"  │ set mantra (figaro e5abf08d) @7",
		" Δ╯",
	)
}

// The question's list is ENCLOSED, so its snake runs the other way: from the
// cursor down to the corner that closes it, and the full body when the
// cursor is elsewhere.
func TestInquirySnakeRunsDownToItsCorner(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.adorned[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(deltaRefOf(inq, 1), false)
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 4),
		"  what does this say?",
		"",
		"Δ datetime  ∅ -> Saturday, September 12, 2026",
		"│ mantra    going to test some harness behavi… -> harness behavior test",
	)
	tr.selectRef(deltaRefOf(inq, 2), false)
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 4),
		"  what does this say?",
		"",
		"  datetime  ∅ -> Saturday, September 12, 2026",
		"Δ mantra    going to test some harness behavi… -> harness behavior test",
	)
}

// Prose hangs its snake off the first line of its own text, and the marker
// changes sides on opening: the gutter while collapsed, the margin while
// open.
func TestProseSnakeHangsFromItsFirstLine(t *testing.T) {
	tr := adornFixture(t)
	ref := nodeRef{turn: 1, index: 1}
	tr.adorned[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	wantRows(t, adornRowsAt(t, tr, "a quote of my own line", 4),
		"Δ a quote of my own line, sliced.",
		"",
		"  datetime  ∅ -> Saturday, September 12, 2026",
		"  mantra    going to test some harness behavi… -> harness behavior test",
	)
	tr.selectRef(deltaRefOf(ref, 2), false)
	wantRows(t, adornRowsAt(t, tr, "a quote of my own line", 4),
		"╭ a quote of my own line, sliced.",
		"│",
		"│ datetime  ∅ -> Saturday, September 12, 2026",
		"Δ mantra    going to test some harness behavi… -> harness behavior test",
	)
}

// ^N/^P WALK THE ROWS ONE AT A TIME. Entering an open list from above lands
// on its first row, from below on its last, and stepping up past the first
// row selects the block, which is where d, t and Enter act.
func TestSelectionWalksEveryDeltaRow(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tool := nodeRef{turn: 1, index: 0}
	prose := nodeRef{turn: 1, index: 1}
	for _, ref := range []nodeRef{inq, tool, prose} {
		tr.adorned[ref] = true
	}
	tr.dropTurnsRows(map[int]struct{}{1: {}})

	want := []nodeRef{
		inq, deltaRefOf(inq, 1), deltaRefOf(inq, 2),
		tool, deltaRefOf(tool, 1), deltaRefOf(tool, 2),
		prose, deltaRefOf(prose, 1), deltaRefOf(prose, 2),
	}
	var got []nodeRef
	for range want {
		tr.selectNode(1, false)
		got = append(got, tr.selection.focus.nodeRef)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop %d: want %+v, got %+v (all %+v)", i, want[i], got[i], got)
		}
	}
	// From below: the last row of the list above, then up through it to the
	// block.
	for i := len(want) - 2; i >= 0; i-- {
		tr.selectNode(-1, false)
		if ref := tr.selection.focus.nodeRef; ref != want[i] {
			t.Fatalf("walking back, stop %d: want %+v, got %+v", i, want[i], ref)
		}
	}
}

// A COLLAPSED LIST OFFERS NO STOPS: there is nothing on screen to select.
func TestClosedListIsNotWalked(t *testing.T) {
	tr := adornFixture(t)
	var got []nodeRef
	for range 3 {
		tr.selectNode(1, false)
		got = append(got, tr.selection.focus.nodeRef)
	}
	want := []nodeRef{{turn: 1, index: inquiryNode}, {turn: 1, index: 0}, {turn: 1, index: 1}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// d, t and Enter: one half each, and both together. Each is inert where it
// has nothing to open.
func TestFoldKeysActOnTheirOwnHalf(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	prose := nodeRef{turn: 1, index: 1}

	tr.selectRef(tool, false)
	tr.key('t')
	if tr.adorned[tool] {
		t.Fatal("t must not open the delta list")
	}
	if !tr.expanded[tool] {
		t.Fatal("t must open the tool's body")
	}
	tr.key('t')
	tr.key('d')
	if !tr.adorned[tool] || tr.expanded[tool] {
		t.Fatalf("d opens the list alone: adorned=%v expanded=%v", tr.adorned[tool], tr.expanded[tool])
	}
	tr.key('d')
	tr.key(0x0d)
	if !tr.adorned[tool] || !tr.expanded[tool] {
		t.Fatalf("Enter opens both: adorned=%v expanded=%v", tr.adorned[tool], tr.expanded[tool])
	}

	// On prose there is no body to open, so t is inert and d still works.
	tr.selectRef(prose, false)
	tr.key('t')
	if tr.expanded[prose] {
		t.Fatal("prose has no body: t must be inert on it")
	}
	tr.key('d')
	if !tr.adorned[prose] {
		t.Fatal("d must open prose's list")
	}

	// d from inside the list closes it and leaves the selection on the block.
	tr.selectRef(deltaRefOf(prose, 2), false)
	tr.key('d')
	if tr.adorned[prose] {
		t.Fatal("d inside the list must close it")
	}
	if ref := tr.selection.focus.nodeRef; ref != prose {
		t.Fatalf("closing the list leaves the selection on the block, got %+v", ref)
	}
}

// WITH NOTHING TO OPEN, d KEEPS ITS OTHER JOB. It was the half-page scroll
// before it was a fold, and a key that answers nothing would have cost the
// motion for free.
func TestDeltaKeyFallsBackToTheHalfPageScroll(t *testing.T) {
	tr := adornFixtureH(t, 10, 4)
	tr.offset = 0
	tr.key('d')
	if tr.offset == 0 {
		t.Fatal("d with no selection must still scroll")
	}
}

// THE WASH STOPS AT THE ADORNMENT'S CHROME. The blank row between a question
// and its rows belongs to the block's coordinate (the snake stands in it) but
// is none of its content, and washing it read as a selection one row taller
// than it was.
func TestWashSkipsTheAdornmentChrome(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.adorned[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(inq, false)
	tr.buildIndex()

	var text, chrome string
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			switch {
			case r.ref == inq && !r.chrome && strings.Contains(stripANSI(r.text), "what does this say?"):
				text = tr.rowLine(r, "", tr.selectionSpan())
			case r.ref == inq && r.chrome:
				chrome = tr.rowLine(r, "", tr.selectionSpan())
			}
		}
	}
	if text == "" || chrome == "" {
		t.Fatal("fixture: the question must have a text row and an adornment chrome row")
	}
	if !strings.Contains(text, selBg) {
		t.Fatalf("the question's own row is washed: %q", text)
	}
	if strings.Contains(chrome, selBg) {
		t.Fatalf("the adornment's chrome row must not be washed: %q", chrome)
	}
}

// A DELTA ROW KEEPS ITS SNAKE UNDER THE WASH, and keeps its own colour: the
// head is how a reader tells which row of a washed list is the focused one,
// and the bar cannot stand in that column as well.
func TestWashedDeltaRowKeepsItsSnake(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.adorned[tool] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(deltaRefOf(tool, 1), false)
	tr.buildIndex()

	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if r.ref != deltaRefOf(tool, 1) {
				continue
			}
			line := tr.rowLine(r, "", tr.selectionSpan())
			if !strings.Contains(line, selBg) {
				t.Fatalf("a selected delta row is washed: %q", line)
			}
			plain := stripANSI(line)
			if !strings.Contains(plain, deltaGlyph) {
				t.Fatalf("the snake's head survives the wash: %q", plain)
			}
			if strings.Contains(plain, "▎") {
				t.Fatalf("the bar must not stand where the snake does: %q", plain)
			}
			return
		}
	}
	t.Fatal("fixture: no delta row found")
}

// A DELTA ROW YANKS WHOLE. What the screen had room for is not what the row
// says: the value is elided on screen and complete on the clipboard.
func TestDeltaRowYanksItsValueWhole(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.adorned[tool] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(deltaRefOf(tool, 2), false)
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("a selected row must yank")
	}
	got, err := selectionText(plan, 64, func(aria.Anchor, int) (aria.Page, error) {
		return aria.Page{}, nil
	})
	if err != nil {
		t.Fatalf("yank: %v", err)
	}
	if !strings.Contains(got, "going to test some harness behavior -> harness behavior test") {
		t.Fatalf("the yank carries the whole transition, got %q", got)
	}
}

// THE COORDINATE MARK NEVER PAINTS OVER THE GUTTER. M-m draws a block's
// address against the right edge, and the marker that says the block carries
// state has to survive it.
func TestCoordinateMarkStopsShortOfTheGutter(t *testing.T) {
	tr := adornFixture(t)
	tr.view.(*ariaView).settings.verbose = true
	tr.buildIndex()
	rows := adornScreen(tr)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Δ") {
		t.Fatalf("the marker is gone under the addresses:\n%s", joined)
	}
	for _, r := range rows {
		if strings.HasSuffix(r, "Δ") && len([]rune(r)) != adornWidth {
			t.Fatalf("the marker must stay in the last column: %q", r)
		}
	}
}

// EVERY ROW IS ONE ROW, AT EVERY WIDTH. An adornment draws in the margins of
// a pane, so a narrow one is where its arithmetic breaks: a row that runs one
// column past the edge wraps in the terminal and desyncs the painter's
// one-row-per-line cursor math.
func TestAdornmentRowsFitEveryWidth(t *testing.T) {
	deltas := adornDeltas(
		"mantra", "going to test some harness behavior", "harness behavior test",
		"日本語のキー", "前の値", "新しい値、日本語のテキストもここにあります",
		"datetime", "", "Saturday, September 12, 2026",
	)
	for w := 1; w <= 120; w++ {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 1, Sealed: true, Inquiry: "q", FormDeltas: deltas,
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: "out", FormDeltas: deltas},
				{Type: livedoc.NodeProse, Markdown: "prose", FormDeltas: deltas},
			},
		}}}}, aria.Notify)
		tr := newTranscript(ldrender.NewFakeTerminal(w, 40), w, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		for _, ref := range []nodeRef{{turn: 1, index: inquiryNode}, {turn: 1, index: 0}, {turn: 1, index: 1}} {
			tr.adorned[ref] = true
		}
		tr.dropTurnsRows(map[int]struct{}{1: {}})
		tr.selectRef(deltaRefOf(nodeRef{turn: 1, index: 0}, 2), false)
		for i, row := range tr.lines() {
			if n := displayWidth(row); n > w {
				t.Fatalf("width %d: row %d is %d columns: %q", w, i, n, stripANSI(row))
			}
		}
	}
}

// A second click on a BLOCK opens what the block has (its body, its list); a
// second click on a delta row opens nothing, because a row is already
// everything it says.
func TestSecondClickOnADeltaRowOpensNothing(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	if !tr.toggleExpansionOf(tool) {
		t.Fatal("a second click on a tool must open it")
	}
	if !tr.adorned[tool] || !tr.expanded[tool] {
		t.Fatalf("a click opens what the block has: adorned=%v expanded=%v", tr.adorned[tool], tr.expanded[tool])
	}
	if tr.toggleExpansionOf(deltaRefOf(tool, 1)) {
		t.Fatal("a second click on a delta row must do nothing")
	}
	if !tr.adorned[tool] {
		t.Fatal("and must not close the list it is in")
	}
}

// A SELECTED BLOCK KEEPS ITS MARKER. The wash used to close with an
// erase-to-end-of-line, and with autowrap off the cursor still stands on the
// last column it wrote: the erase wiped the gutter glyph, and the last
// character of any row that filled the pane with it. Found by replaying the
// pager into a real pty (adornment_pty_test.go).
func TestSelectedBlockKeepsItsGutterMarker(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := adornFixture(t)
	prose := nodeRef{turn: 1, index: 1}
	tr.selectRef(prose, false)
	tr.buildIndex()

	seen := false
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if r.ref != prose || r.gutter == "" {
				continue
			}
			seen = true
			line := tr.rowLine(r, "", tr.selectionSpan())
			if !strings.HasSuffix(stripANSI(line), deltaGlyph) {
				t.Fatalf("the marker must survive the wash: %q", stripANSI(line))
			}
			if strings.Contains(line, "\x1b[K") {
				t.Fatalf("a row that fills the pane must not erase its own last column: %q", line)
			}
		}
	}
	if !seen {
		t.Fatal("fixture: no row carries the collapsed marker")
	}
	// A row that does NOT reach the edge still carries the wash to it: the
	// tool's output line stops where its output does.
	tool := nodeRef{turn: 1, index: 0}
	tr.selectRef(tool, false)
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if r.ref != tool || !strings.Contains(stripANSI(r.text), "set mantra (figaro") {
				continue
			}
			if line := tr.rowLine(r, "", tr.selectionSpan()); !strings.Contains(line, "\x1b[K") {
				t.Fatalf("a short row must wash to the edge: %q", line)
			}
			return
		}
	}
	t.Fatal("fixture: no short row to wash")
}
