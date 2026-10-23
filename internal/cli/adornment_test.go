package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// Rows are compared plain: glyphs and columns are the claim; colours have their
// own tests.

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

func adornFixture(t testing.TB) *transcript { return adornFixtureH(t, 40, 1) }

// adornFixtureH repeats the turn at a viewport height short enough to make a
// scroll observable.
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
				Args:       map[string]any{"command": "figaro set mantra"},
				Output:     "set mantra (figaro e5abf08d) @7",
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

// Collapsed: one glyph in the right gutter; the tool on its header, prose on the
// end of its text, the question on the voice row.
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
	if !strings.HasSuffix(rows[0], "Δ") || !strings.Contains(rows[0], "> input ⑂ "+ariaName("e5abf08d")) {
		t.Fatalf("the question's fork rides its header row, the marker its gutter: %q", rows[0])
	}
}

// A lifted fork leaves the list: the header says it once.
func TestInquiryLiftsTheForkOutOfItsList(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.metaOpen[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 5),
		"  what does this say?",
		"•",
		"│ address   1",
		"│ datetime  ∅ -> Saturday, September 12, 2026",
		"│ mantra    going to test some harness behavi… -> harness behavior test",
	)
	rows := adornScreen(tr)
	if strings.Contains(strings.Join(rows, "\n"), "forked_from") {
		t.Fatalf("the lifted fork left a row behind:\n%s", strings.Join(rows, "\n"))
	}
	for _, r := range rows {
		if strings.HasPrefix(r, "╰") {
			return
		}
	}
	t.Fatalf("the question's list must close on the rule:\n%s", strings.Join(rows, "\n"))
}

// Only the question lifts a fork; under a node's coordinate it is a delta like any
// other.
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
	rows := deltaRows(deltas, adornLift(0))
	if len(rows) < 2 || !strings.Contains(deltaRowFull(rows[1]), "system.forked_from") {
		t.Fatalf("a node's fork row says what it is: %+v", rows)
	}
}

// Open with nothing selected the head parks at the anchor; with a row selected the
// body runs from the anchor down to it.
func TestToolSnakeFollowsTheSelection(t *testing.T) {
	tr := adornFixture(t)
	ref := nodeRef{turn: 1, index: 0}
	tr.metaOpen[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})

	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 4),
		"  │ set mantra (figaro e5abf08d) @7",
		" •╯",
		"   address   1.0",
		"   tool      bash",
	)

	tr.selectRef(metaRefOf(ref, 1), false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 4),
		"  │ set mantra (figaro e5abf08d) @7",
		" ╭╯",
		" • address   1.0",
		"   tool      bash",
	)

	tr.selectRef(metaRefOf(ref, 6), false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 8),
		"  │ set mantra (figaro e5abf08d) @7",
		" ╭╯",
		" │ address   1.0",
		" │ tool      bash",
		" │ command   figaro set mantra",
		" │ status    ok",
		" │ datetime  ∅ -> Saturday, September 12, 2026",
		" Δ mantra    going to test some harness behav… -> harness behavior test",
	)

	tr.selectRef(nodeRef{turn: 1, index: 1}, false)
	wantRows(t, adornRowsAt(t, tr, "set mantra (figaro", 2),
		"  │ set mantra (figaro e5abf08d) @7",
		" •╯",
	)
}

// An enclosed list's snake runs from the cursor down to the closing corner, and
// whole when the cursor is elsewhere.
func TestInquirySnakeRunsDownToItsCorner(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.metaOpen[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(metaRefOf(inq, 2), false)
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 5),
		"  what does this say?",
		"",
		"  address   1",
		"Δ datetime  ∅ -> Saturday, September 12, 2026",
		"│ mantra    going to test some harness behavi… -> harness behavior test",
	)
	tr.selectRef(metaRefOf(inq, 3), false)
	wantRows(t, adornRowsAt(t, tr, "what does this say?", 5),
		"  what does this say?",
		"",
		"  address   1",
		"  datetime  ∅ -> Saturday, September 12, 2026",
		"Δ mantra    going to test some harness behavi… -> harness behavior test",
	)
}

// Prose hangs its snake off its first line; the marker moves from gutter to margin
// on opening.
func TestProseSnakeHangsFromItsFirstLine(t *testing.T) {
	tr := adornFixture(t)
	ref := nodeRef{turn: 1, index: 1}
	tr.metaOpen[ref] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	wantRows(t, adornRowsAt(t, tr, "a quote of my own line", 5),
		"• a quote of my own line, sliced.",
		"",
		"  address   1.1",
		"  datetime  ∅ -> Saturday, September 12, 2026",
		"  mantra    going to test some harness behavi… -> harness behavior test",
	)
	tr.selectRef(metaRefOf(ref, 3), false)
	wantRows(t, adornRowsAt(t, tr, "a quote of my own line", 5),
		"╭ a quote of my own line, sliced.",
		"│",
		"│ address   1.1",
		"│ datetime  ∅ -> Saturday, September 12, 2026",
		"Δ mantra    going to test some harness behavi… -> harness behavior test",
	)
}

// Entering an open list from above lands on its first row, from below on its last;
// up past the first row selects the block.
func TestSelectionWalksEveryDeltaRow(t *testing.T) {
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tool := nodeRef{turn: 1, index: 0}
	prose := nodeRef{turn: 1, index: 1}
	for _, ref := range []nodeRef{inq, tool, prose} {
		tr.metaOpen[ref] = true
	}
	tr.dropTurnsRows(map[int]struct{}{1: {}})

	want := []nodeRef{inq}
	for i := 1; i <= 3; i++ {
		want = append(want, metaRefOf(inq, i))
	}
	want = append(want, tool)
	for i := 1; i <= 6; i++ {
		want = append(want, metaRefOf(tool, i))
	}
	want = append(want, prose)
	for i := 1; i <= 3; i++ {
		want = append(want, metaRefOf(prose, i))
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
	for i := len(want) - 2; i >= 0; i-- {
		tr.selectNode(-1, false)
		if ref := tr.selection.focus.nodeRef; ref != want[i] {
			t.Fatalf("walking back, stop %d: want %+v, got %+v", i, want[i], ref)
		}
	}
}

// A collapsed list offers no stops.
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

// Enter is the only key that opens a form-delta list.
func TestFoldKeysActOnTheirOwnHalf(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	prose := nodeRef{turn: 1, index: 1}

	tr.selectRef(tool, false)
	tr.key('e')
	if tr.metaOpen[tool] {
		t.Fatal("e must not open the delta list")
	}
	if !tr.expanded[tool] {
		t.Fatal("e must open the tool's body")
	}
	tr.key('e')
	tr.key(0x0d)
	if !tr.metaOpen[tool] || !tr.expanded[tool] {
		t.Fatalf("Enter opens both: adorned=%v expanded=%v", tr.metaOpen[tool], tr.expanded[tool])
	}

	tr.selectRef(prose, false)
	tr.key('e')
	if tr.expanded[prose] {
		t.Fatal("prose has no body: t must be inert on it")
	}
	tr.key(0x0d)
	if !tr.metaOpen[prose] {
		t.Fatal("Enter must open prose's list")
	}

	tr.selectRef(metaRefOf(prose, 2), false)
	tr.key(0x0d)
	if !tr.metaOpen[prose] || !tr.entryOpen[metaRefOf(prose, 2)] {
		t.Fatal("Enter inside the list opens the entry and leaves the list open")
	}
	tr.selectRef(prose, false)
	tr.key(0x0d)
	if tr.metaOpen[prose] {
		t.Fatal("Enter on the block closes its list")
	}
}

// 'd' is a motion unconditionally, whether or not the selection carries state.
func TestDeltaKeyIsTheHalfPageScroll(t *testing.T) {
	tr := adornFixtureH(t, 10, 4)
	tr.offset = 0
	tr.key('d')
	if tr.offset == 0 {
		t.Fatal("d with no selection must still scroll")
	}
	tr.offset = 0
	ref := nodeRef{turn: 1, index: 1}
	tr.selectRef(ref, false)
	tr.key('d')
	if tr.metaOpen[ref] {
		t.Fatal("d must not open a form-delta list")
	}
	if tr.offset == 0 {
		t.Fatal("d over a block with state must still scroll")
	}
}

// The wash covers the whole block, chrome included; the snake head says which row
// the cursor is on.
func TestWashCoversTheBlocksChrome(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := adornFixture(t)
	inq := nodeRef{turn: 1, index: inquiryNode}
	tr.metaOpen[inq] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(inq, false)
	tr.buildIndex()

	var text, chrome string
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			switch {
			case r.ref == inq && !r.chrome && strings.Contains(stripANSI(r.text), "what does this say?"):
				text = tr.rowLine(r, "", tr.selectionSpan(), -1)
			case r.ref == inq && r.chrome:
				chrome = tr.rowLine(r, "", tr.selectionSpan(), -1)
			}
		}
	}
	if text == "" || chrome == "" {
		t.Fatal("fixture: the question must have a text row and an adornment chrome row")
	}
	for _, row := range []string{text, chrome} {
		if !washed(row) {
			t.Fatalf("every row of the selected block is washed: %q", row)
		}
	}
}

// A delta row keeps its snake and its own colour under the wash.
func TestWashedDeltaRowKeepsItsSnake(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.metaOpen[tool] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(metaRefOf(tool, 1), false)
	tr.buildIndex()

	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if r.ref != metaRefOf(tool, 1) {
				continue
			}
			line := tr.rowLine(r, "", tr.selectionSpan(), -1)
			if !washed(line) {
				t.Fatalf("a selected delta row is washed: %q", line)
			}
			plain := stripANSI(line)
			if !strings.Contains(plain, headGlyph) {
				t.Fatalf("the snake's head survives the wash: %q", plain)
			}
			return
		}
	}
	t.Fatal("fixture: no delta row found")
}

// A delta row yanks whole: elided on screen, complete on the clipboard.
func TestDeltaRowYanksItsValueWhole(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.metaOpen[tool] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.selectRef(metaRefOf(tool, 6), false)
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

// The coordinate mark never paints over the gutter.
func TestCoordinateMarkStopsShortOfTheGutter(t *testing.T) {
	tr := adornFixture(t)
	tr.view.(*ariaView).settings.more = true
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

// Every row is one row at every width: one column past the edge wraps and desyncs
// the painter's cursor math.
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
				{Type: livedoc.NodeThinking, Markdown: "thinking", FormDeltas: deltas},
			},
		}}}}, aria.Notify)
		tr := newTranscript(ldrender.NewFakeTerminal(w, 40), w, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		for _, ref := range []nodeRef{{turn: 1, index: inquiryNode}, {turn: 1, index: 0}, {turn: 1, index: 1}, {turn: 1, index: 2}} {
			tr.metaOpen[ref] = true
		}
		tr.dropTurnsRows(map[int]struct{}{1: {}})
		tr.selectRef(metaRefOf(nodeRef{turn: 1, index: 0}, 2), false)
		for i, row := range tr.lines() {
			if n := displayWidth(row); n > w {
				t.Fatalf("width %d: row %d is %d columns: %q", w, i, n, stripANSI(row))
			}
		}
	}
}

// A second click on a block opens what it has; on a delta row, that row.
func TestSecondClickOnADeltaRowOpensTheRow(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	if !tr.toggleExpansionOf(tool) {
		t.Fatal("a second click on a tool must open it")
	}
	if !tr.metaOpen[tool] || !tr.expanded[tool] {
		t.Fatalf("a click opens what the block has: adorned=%v expanded=%v", tr.metaOpen[tool], tr.expanded[tool])
	}
	if !tr.toggleExpansionOf(metaRefOf(tool, 5)) || !tr.entryOpen[metaRefOf(tool, 5)] {
		t.Fatal("a second click on a delta row opens it")
	}
	if !tr.metaOpen[tool] {
		t.Fatal("and must not close the list it is in")
	}
}

// With autowrap off the cursor stands on the last column written, so a closing
// erase-to-end-of-line wipes the gutter glyph.
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
			line := tr.rowLine(r, "", tr.selectionSpan(), -1)
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
	// A row that does not reach the edge still carries the wash to it.
	tool := nodeRef{turn: 1, index: 0}
	tr.selectRef(tool, false)
	for _, e := range tr.index.entries {
		for _, r := range e.rows {
			if r.ref != tool || !strings.Contains(stripANSI(r.text), "set mantra (figaro") {
				continue
			}
			if line := tr.rowLine(r, "", tr.selectionSpan(), -1); displayWidth(line) != tr.w {
				t.Fatalf("a short row must wash to the edge: %d of %d cells: %q", displayWidth(line), tr.w, line)
			}
			return
		}
	}
	t.Fatal("fixture: no short row to wash")
}

func stickyAdorned(t testing.TB) (*transcript, nodeRef) {
	t.Helper()
	tr := adornFixtureH(t, 8, 3)
	tr.view.(*ariaView).settings.sticky = true
	inq := nodeRef{turn: 3, index: inquiryNode}
	tr.metaOpen[inq] = true
	tr.dropTurnsRows(map[int]struct{}{3: {}})
	tr.buildIndex()
	return tr, inq
}

// The header covers rows, so the cursor must clear it: the scroll floor is not the
// offset.
func TestSelectedRowClearsTheStickyHeader(t *testing.T) {
	tr, inq := stickyAdorned(t)
	tr.selectRef(metaRefOf(inq, 2), false)

	span, ok := tr.nodeSpanOf(tr.selection.focus.nodeRef)
	if !ok {
		t.Fatal("fixture: the delta row has no span")
	}
	tr.offset = span.first
	tr.render()
	if tr.headRows() == 0 {
		t.Fatal("fixture: the header is not pinned, so nothing is covered")
	}
	tr.ensureSelectionVisible()
	if head := tr.headRows(); span.first < tr.offset+head {
		t.Fatalf("the focused row is under the header: row %d, offset %d, header %d",
			span.first, tr.offset, head)
	}

	for range 2 {
		tr.key(0x10) // ^P
		span, ok := tr.nodeSpanOf(tr.selection.focus.nodeRef)
		if !ok {
			t.Fatal("the focus lost its span")
		}
		if head := tr.headRows(); span.first < tr.offset+head {
			t.Fatalf("^P parked %+v under the header: row %d, offset %d, header %d",
				tr.selection.focus.nodeRef, span.first, tr.offset, head)
		}
	}
}

// The band stops at the corner the snake closes into, which is a rule row wearing
// a corner glyph.
func TestWashStopsAtTheAdornmentsCorner(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	tr, inq := stickyAdorned(t)
	tr.selectRef(metaRefOf(inq, 2), false)
	tr.ensureSelectionVisible()

	rows := tr.window(tr.offset, tr.offset+8, nil)
	corner, voice := -1, -1
	for i, r := range rows {
		switch plain := strings.TrimSpace(stripANSI(r)); {
		case strings.HasPrefix(plain, "╰─"):
			corner = i
		case plain == "< figaro":
			voice = i
		}
	}
	if corner < 0 || voice < 0 {
		t.Fatalf("fixture: want a corner and a voice header:\n%s", strings.Join(stripANSIAll(rows), "\n"))
	}
	if !washed(rows[corner]) {
		t.Errorf("the corner closes the box and belongs to the band: %q", stripANSI(rows[corner]))
	}
	if washed(rows[voice]) {
		t.Errorf("the wash ran past the corner onto %q", stripANSI(rows[voice]))
	}
}

func adornRowCount(deltas map[string]livedoc.FormDelta, lift bool) int {
	return deltaRowCount(deltas, lift, lift && forkParentOf(deltas) != "")
}
