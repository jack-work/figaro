package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// coordFixture is a two-turn aria with stamped nodes, rendered through the
// pager at a width wide enough that nothing clips.
func coordFixture(tb testing.TB, verbose bool) *transcript {
	tb.Helper()
	at := time.Date(2026, 7, 27, 1, 23, 45, 0, time.Local).UnixMilli()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 7, Inquiry: "QUESTIONSEVEN", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "SEVENANSWER", At: at},
		}}},
		{Turn: aria.Turn{ID: 8, Inquiry: "QUESTIONEIGHT", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "EIGHTANSWER", At: at + 61_000},
			{Type: livedoc.NodeTool, ID: "t1", Name: "bash", Status: livedoc.StatusOK,
				Summary: "ls", Output: "out", StartedAt: at + 122_000},
		}}},
	}}, aria.Notify)
	ft := ldrender.NewFakeTerminal(60, 40)
	tr := newTranscript(ft, 60, 40, &ariaView{settings: &renderSettings{verbose: verbose, coordFormat: "15:04:05"}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	return tr
}

func plainRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimSpace(stripANSI(r))
	}
	return out
}

// TestAddressesAppearOnlyUnderVerbose is the headline: ^O off, the transcript
// is what it always was; ^O on, every block states its address against the
// right edge, and not one row moves.
func TestAddressesAppearOnlyUnderVerbose(t *testing.T) {
	off := plainRows(coordFixture(t, false).lines())
	for _, r := range off {
		if addressRE.MatchString(r) {
			t.Fatalf("an address appeared with ^O off: %q\n%s", r, strings.Join(off, "\n"))
		}
	}

	on := plainRows(coordFixture(t, true).lines())
	if len(on) != len(off) {
		t.Fatalf("^O moved the conversation: %d rows on, %d off", len(on), len(off))
	}
	want := []string{
		"7",              // turn 7's question, named by its turn
		"7.0 · 01:23:45", // its one prose node
		"8",              // turn 8's question
		"8.0 · 01:24:46", // prose
		"8.1 · 01:25:47", // the tool, stamped from StartedAt
	}
	var got []string
	for i, r := range on {
		if m := addressRE.FindString(r); m != "" {
			got = append(got, m)
			if strings.TrimSpace(strings.TrimSuffix(r, m)) != strings.TrimSpace(off[i]) {
				t.Fatalf("row %d changed under ^O:\n on  %q\n off %q", i, r, off[i])
			}
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("addresses:\n got %q\nwant %q\n--- rows ---\n%s",
			got, want, strings.Join(on, "\n"))
	}
}

// addressRE matches a rendered address at the end of a row: the turn, the node
// when there is one, and the time it was written.
var addressRE = regexp.MustCompile(`\d+(\.\d+)?( · \d\d:\d\d:\d\d)?$`)

// TestAddressedRowIsOnePhysicalRow: an address must survive a pane too narrow
// to hold it by being clipped, never by wrapping, and it may never widen the
// row it rides.
func TestAddressedRowIsOnePhysicalRow(t *testing.T) {
	for _, w := range []int{4, 8, 12, 60} {
		tr := coordFixture(t, true)
		tr.resize(w, 40)
		for i, r := range tr.lines() {
			if strings.ContainsAny(r, "\n\r\t") {
				t.Fatalf("w=%d row %d contains a control character: %q", w, i, r)
			}
			if !addressRE.MatchString(strings.TrimSpace(stripANSI(r))) {
				continue // only addressed rows are this test's business
			}
			if got := runewidth.StringWidth(stripANSI(r)); got > w {
				t.Fatalf("w=%d row %d is %d columns wide: %q", w, i, got, r)
			}
		}
	}
}

// TestAddressRidesTheNodesOwnRow: the address is drawn on the node's first row,
// so a node's span is what it was without ^O and scroll-into-view cannot land
// one row off.
func TestAddressRidesTheNodesOwnRow(t *testing.T) {
	on, off := coordFixture(t, true), coordFixture(t, false)
	on.buildIndex()
	off.buildIndex()
	ref := nodeRef{turn: 8, index: 1} // the tool
	span, ok := on.nodeSpanOf(ref)
	if !ok {
		t.Fatal("no span for 8.1")
	}
	plain, _ := off.nodeSpanOf(ref)
	if span != plain {
		t.Fatalf("^O moved the node's span: %+v, want %+v", span, plain)
	}
	first := strings.TrimSpace(stripANSI(on.lines()[span.first]))
	if !strings.HasSuffix(first, "8.1 · 01:25:47") {
		t.Fatalf("the node's first row does not carry its address: %q", first)
	}
	// And it is genuinely part of the node: the row takes the selection cue.
	on.selection = nodeSelection{active: true,
		anchor: selectionPoint{nodeRef: ref}, focus: selectionPoint{nodeRef: ref}}
	if !strings.Contains(stripANSI(on.lines()[span.first]), "▎") {
		t.Fatalf("the addressed row took no selection cue: %q", on.lines()[span.first])
	}
}

// TestAddressesLeaveTheChromeAlone: no row is inserted, so the rule stays the
// overline of the header beneath it under ^O as well.
func TestAddressesLeaveTheChromeAlone(t *testing.T) {
	assertNoGapBelowRule(t, "transcript verbose", coordFixture(t, true).lines())

	rows := plainRows(coordFixture(t, true).lines())
	i := -1
	for k, r := range rows {
		if strings.Contains(r, "QUESTIONEIGHT") {
			i = k
			break
		}
	}
	if i < 2 {
		t.Fatalf("no question in:\n%s", strings.Join(rows, "\n"))
	}
	if got := []string{rows[i-2], rows[i-1]}; got[0] != "> input" || got[1] != "" {
		t.Fatalf("question chrome under ^O: %q, want the header and a blank", got)
	}
	if !strings.HasSuffix(rows[i], "8") {
		t.Fatalf("the question does not carry its turn: %q", rows[i])
	}
}

// TestCoordinateLabelShapes pins the formatter directly, including the two
// edges: the question, named by its turn alone, and an unstamped node, which
// prints no time rather than 1970.
func TestCoordinateLabelShapes(t *testing.T) {
	at := time.Date(2026, 7, 27, 1, 23, 45, 0, time.Local).UnixMilli()
	cases := []struct {
		turn, node int
		at         int64
		layout     string
		want       string
	}{
		{12, 3, at, "15:04:05", "12.3 · 01:23:45"},
		{12, 3, at, "", "12.3 · 27/07/26 01:23:45"},
		{12, inquiryNode, 0, "", "12"},
		{1, 0, 0, "", "1.0"},
	}
	for _, c := range cases {
		if got := coordLabel(c.turn, c.node, c.at, c.layout); got != c.want {
			t.Errorf("coordLabel(%d,%d,%d,%q) = %q, want %q", c.turn, c.node, c.at, c.layout, got, c.want)
		}
	}
}

// TestAddressesSurviveToggling: the address is composed with the row and drawn
// only when asked, so flipping ^O cannot leave half a screen in one mode and
// half in the other, whatever the row cache is holding.
func TestAddressesSurviveToggling(t *testing.T) {
	tr := coordFixture(t, false)
	set := tr.view.(*ariaView).settings

	off := plainRows(tr.lines())
	for range 3 {
		set.verbose = true
		on := plainRows(tr.lines())
		if len(on) != len(off) {
			t.Fatalf("^O moved the conversation: %d rows on, %d off", len(on), len(off))
		}
		var addressed int
		for i, r := range on {
			if r == off[i] {
				continue
			}
			addressed++
			if !addressRE.MatchString(r) {
				t.Fatalf("row %d changed under ^O without gaining an address:\n on  %q\n off %q", i, r, off[i])
			}
		}
		if addressed == 0 {
			t.Fatal("^O drew no addresses at all")
		}
		set.verbose = false
		if again := plainRows(tr.lines()); strings.Join(again, "\n") != strings.Join(off, "\n") {
			for i := range again {
				if again[i] != off[i] {
					t.Fatalf("row %d kept an address after ^O went off:\n got %q\nwant %q", i, again[i], off[i])
				}
			}
		}
	}
}
