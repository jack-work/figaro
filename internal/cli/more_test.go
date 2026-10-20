package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

const moreSender = "c0467c8d3826eff2b8d2d34fcda29517"

func morePager(t *testing.T) (*interactiveInput, *livelogTurn, *paneWriter) {
	t.Helper()
	out := &paneWriter{}
	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(out, 100, 30, settings, "aria0001", time.Unix(0, 0), status, nil, nil)
	lt.enterTranscript()
	turn := aria.Turn{
		ID: 1, Sealed: true, Inquiry: "a question from another aria",
		InquirySegments: []aria.InquirySegment{{Sender: rpc.AriaLabelPrefix + moreSender, Text: "a question from another aria"}},
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "a short answer", At: 1_700_000_000_000},
			{Type: livedoc.NodeProse, Markdown: strings.Repeat("a paragraph that runs to the edge ", 3), At: 1_700_000_000_000},
		},
	}
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: turn}}})
	in := &interactiveInput{
		lt: lt, fcli: stubHistoryClient{}, mu: new(renderLock), set: settings,
		figaroID: "aria0001", cancel: func() {}, disconnectCh: make(chan struct{}, 1),
	}
	lt.tr.render()
	out.take()
	return in, lt, out
}

// M-m through the real input loop: the other name joins the sender, the
// address fills the blank columns, and nothing is laid out again: no row is
// added, no row cache is dropped, and only rows that changed are written.
func TestMoreRepaintsOnlyTheRowsItChanges(t *testing.T) {
	withShownIDs(t, idsAsHandles)
	in, lt, out := morePager(t)
	tr := lt.tr
	total, rev := tr.index.total, tr.rowRev
	before := stripANSIAll(tr.lines())

	clock := time.Unix(100, 0)
	tr.now = func() time.Time { return clock }
	tr.lastFull = clock
	clock = clock.Add(time.Minute)

	feed(t, in, "\x1bm")
	if !in.set.more {
		t.Fatal("M-m did not turn more on")
	}
	frame := out.take()
	after := stripANSIAll(tr.lines())
	if tr.index.total != total || tr.rowRev != rev {
		t.Fatalf("more laid the transcript out again: total %d -> %d, rowRev %d -> %d", total, tr.index.total, rev, tr.rowRev)
	}
	changed := map[int]bool{}
	for i := range before {
		if before[i] != after[i] {
			changed[i+1] = true
		}
	}
	written := rowsWritten(frame)
	if len(written) == 0 || len(written) > len(changed) {
		t.Fatalf("more wrote %d rows for %d that changed (a resync repaints them all):\n%q", len(written), len(changed), frame)
	}
	head := after[findLine(after, "> figaro")]
	if want := ariaHandle(moreSender) + " (" + shortHex(moreSender) + ")"; !strings.Contains(head, want) {
		t.Fatalf("the sender's other name is missing: %q", head)
	}
	short := after[findLine(after, "a short answer")]
	if !strings.Contains(short, "1.0 · ") || !strings.HasPrefix(short, "  a short answer ") {
		t.Fatalf("the address does not fill the blank columns: %q", short)
	}
	long := after[findLine(after, "runs to the edge")]
	if long != before[findLine(before, "runs to the edge")] {
		t.Fatalf("an address was painted over text:\nbefore %q\nafter  %q", before[findLine(before, "runs to the edge")], long)
	}

	feed(t, in, "\x1bm")
	if got := stripANSIAll(tr.lines()); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Fatalf("more off did not restore the screen:\n%s", strings.Join(got, "\n"))
	}
}

// M-m in the incipit stays in the incipit.
func TestMoreDoesNotOpenThePager(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, false)
	feed(t, in, "\x1bm")
	if lt.transcriptActive() {
		t.Fatal("M-m opened the pager")
	}
	if !in.set.more {
		t.Fatal("M-m did not turn more on")
	}
}

func TestFillRightTakesOnlyBlankColumns(t *testing.T) {
	line := "  a row of thirty-six columns, here"
	if got := ldrender.FillRight("  text", "1.0", 34); got != "  text"+strings.Repeat(" ", 25)+"1.0" {
		t.Fatalf("a tag that fits stands against the edge: %q", got)
	}
	got := ldrender.FillRight(line, "1.0 · 19/10/26 19:38:39", 50)
	if !strings.HasPrefix(got, line+" ") || !strings.HasSuffix(got, "…") || ldrender.Width(got) != 50 {
		t.Fatalf("a tag with too little room is cut, never the text: %q", got)
	}
	if got := ldrender.FillRight(line, "1.0", 38); got != line {
		t.Fatalf("a tag with no room is left out: %q", got)
	}
}

// A collapsed entry is one row: a value with a newline shows its first line
// and an ellipsis, as does one wider than the pane.
func TestACollapsedEntryIsOneLine(t *testing.T) {
	multi := metaEntry{key: "command", value: "echo one\necho two"}
	rows := entryRows(multi, 7, 60, false, nil)
	if len(rows) != 1 || !strings.HasSuffix(rows[0].text, "echo one…") {
		t.Fatalf("a multi-line value is not one line ending in an ellipsis: %+v", rows)
	}
	wide := metaEntry{key: "command", value: strings.Repeat("abcdefghij ", 12)}
	if rows := entryRows(wide, 7, 60, false, nil); len(rows) != 1 || !strings.HasSuffix(rows[0].text, "…") {
		t.Fatalf("a wide value is not one elided line: %+v", rows)
	}
	open := entryRows(multi, 7, 60, true, nil)
	if len(open) != 2 || !strings.HasSuffix(open[0].text, "echo one") || strings.TrimSpace(open[1].text) != "echo two" {
		t.Fatalf("an open entry shows every line under the value column: %+v", open)
	}
}

// Enter on an entry opens that entry's text. It neither closes the section
// nor touches the block's own fold.
func TestEnterOnAnEntryExpandsItsText(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.selectRef(tool, false)
	tr.key(0x0d)
	if !tr.metaOpen[tool] || !tr.expanded[tool] {
		t.Fatal("fixture: Enter on the block opens its body and its section")
	}
	mantra := metaRefOf(tool, 6)
	tr.selectRef(mantra, false)
	rows := tr.index.total
	tr.key(0x0d)
	if !tr.entryOpen[mantra] || !tr.metaOpen[tool] || !tr.expanded[tool] {
		t.Fatalf("Enter on an entry: entry %v section %v body %v", tr.entryOpen[mantra], tr.metaOpen[tool], tr.expanded[tool])
	}
	tr.buildIndex()
	if tr.index.total <= rows {
		t.Fatal("an open delta entry shows its two sides under it")
	}
	screen := strings.Join(adornScreen(tr), "\n")
	if !strings.Contains(screen, "- going to test some harness behavior") || !strings.Contains(screen, "+ harness behavior test") {
		t.Fatalf("the open entry does not show both sides whole:\n%s", screen)
	}
	if got := tr.selection.focus.nodeRef; got != mantra {
		t.Fatalf("opening moved the selection to %+v", got)
	}
	tr.key(0x0d)
	if tr.entryOpen[mantra] || !tr.metaOpen[tool] {
		t.Fatal("a second Enter must close the entry and leave the section open")
	}
}

// More annotates an open section and adds no row to it: an open entry stays
// as tall as it was.
func TestMoreAddsNoRowsToAnOpenSection(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.metaOpen[tool] = true
	tr.openEntries()[metaRefOf(tool, 6)] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	tr.buildIndex()
	total, rev := tr.index.total, tr.rowRev
	tr.view.(*ariaView).settings.more = true
	tr.buildIndex()
	if tr.index.total != total || tr.rowRev != rev {
		t.Fatalf("more changed the layout: %d -> %d rows", total, tr.index.total)
	}
	screen := adornScreen(tr)
	if i := findLine(screen, "address"); i < 0 || !strings.HasSuffix(screen[i], "meta") {
		t.Fatalf("more does not label the first metadata row:\n%s", strings.Join(screen, "\n"))
	}
	if i := findLine(screen, "datetime"); i < 0 || !strings.HasSuffix(screen[i], "delta") {
		t.Fatalf("more does not label the first delta row:\n%s", strings.Join(screen, "\n"))
	}
}

// The snake's head says what kind of row the cursor is on.
func TestTheHeadNamesTheKindOfRow(t *testing.T) {
	tr := adornFixture(t)
	tool := nodeRef{turn: 1, index: 0}
	tr.metaOpen[tool] = true
	tr.dropTurnsRows(map[int]struct{}{1: {}})
	for _, tc := range []struct {
		meta int
		key  string
		head string
	}{{1, "address", headGlyph}, {5, "datetime", deltaGlyph}} {
		tr.selectRef(metaRefOf(tool, tc.meta), false)
		screen := adornScreen(tr)
		i := findLine(screen, tc.key)
		if i < 0 || !strings.HasPrefix(screen[i], " "+tc.head+" ") {
			t.Fatalf("on %s the head is not %s: %q", tc.key, tc.head, screen[max(i, 0)])
		}
	}
}

// A block's metadata is one list: its fields, then the state it changed, then
// the comments that end on it. Closed, it is one mark in the gutter; open, one
// section under one snake.
func TestOneSectionCarriesFieldsDeltasAndComments(t *testing.T) {
	tr, _ := sourceFixture(t)
	tr.setComments(readComments(commentBoard(t, map[string]commentRecord{
		"c1": {At: "<412.0:4-9>!", Turn: 3, Thread: one("gluck", "why retry?")},
	})))
	prose := nodeRef{turn: 3, index: 0}
	entries := tr.entriesAt(prose)
	if len(entries) < 2 || entries[0].kind != metaField || entries[0].key != "address" || entries[len(entries)-1].kind != metaComment {
		t.Fatalf("want the address first and the comment last: %+v", entries)
	}
	closed := stripAll(paintedLines(tr))
	if findLine(closed, "▸ gluck") >= 0 {
		t.Fatalf("a closed section drew its comment:\n%s", strings.Join(closed, "\n"))
	}
	if i := findLine(closed, "is width."); i < 0 || !strings.HasSuffix(strings.TrimRight(closed[i], " "), commentGlyph) {
		t.Fatalf("a closed section with a comment wears %s in the gutter:\n%s", commentGlyph, strings.Join(closed, "\n"))
	}
	openSection(tr, prose)
	tr.selectRef(metaRefOf(prose, len(entries)), false)
	open := stripAll(paintedLines(tr))
	addr, note := findLine(open, "address"), findLine(open, "▸ gluck")
	if addr < 0 || note != addr+1 {
		t.Fatalf("the comment is not the entry after the address:\n%s", strings.Join(open, "\n"))
	}
	if !strings.HasPrefix(open[addr], snakeBody) || !strings.HasPrefix(open[note], commentGlyph) {
		t.Fatalf("one snake runs from the block to the comment:\n%s", strings.Join(open[addr-2:note+1], "\n"))
	}
}

// A folded comment too wide for its row keeps its address and ends its text
// in an ellipsis.
func TestAFoldedCommentIsOneElidedLine(t *testing.T) {
	c := comment{id: "k1", thread: []commentEntry{{key: "a", by: "gluck", text: strings.Repeat("a long comment ", 10)}}}
	rows := commentEntryRows(c, false, 60, nil)
	plain := stripANSI(rows[0].text)
	if len(rows) != 1 || !strings.Contains(plain, "…") || !strings.HasSuffix(plain, "#k1") || ldrender.Width(plain) > 60 {
		t.Fatalf("a folded comment is not one elided row that keeps its address: %q", plain)
	}
}
