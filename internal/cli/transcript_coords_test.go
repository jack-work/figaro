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
	}})
	ft := ldrender.NewFakeTerminal(60, 40)
	tr := newTranscript(ft, 60, 40, &ariaView{settings: &renderSettings{verbose: verbose}},
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

// TestCoordinatesAppearOnlyUnderVerbose is the headline: Ctrl-O off, the
// transcript is byte-identical to what it always was; Ctrl-O on, every node
// and the inquiry gain exactly one address row.
func TestCoordinatesAppearOnlyUnderVerbose(t *testing.T) {
	off := plainRows(coordFixture(t, false).lines())
	for _, r := range off {
		if coordRowRE.MatchString(r) {
			t.Fatalf("a coordinate row appeared with verbose OFF: %q\n%s", r, strings.Join(off, "\n"))
		}
	}

	on := plainRows(coordFixture(t, true).lines())
	want := []string{
		"7.-1",           // turn 7's question, at the virtual node id
		"7.0 · 01:23:45", // its one prose node
		"8.-1",           // turn 8's question
		"8.0 · 01:24:46", // prose
		"8.1 · 01:25:47", // the tool, stamped from StartedAt
	}
	var got []string
	for _, r := range on {
		if coordRowRE.MatchString(r) {
			got = append(got, r)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("coordinate rows:\n got %q\nwant %q\n--- rows ---\n%s",
			got, want, strings.Join(on, "\n"))
	}
}

// coordRowRE matches a rendered coordinate row and nothing else: an address,
// optionally a middot and a clock time, alone on the line.
var coordRowRE = regexp.MustCompile(`^-?\d+\.-?\d+( · \d\d:\d\d:\d\d)?$`)

// TestCoordinateRowIsOnePhysicalRow: a coordinate must survive a pane too
// narrow to hold it by being CLIPPED, never by wrapping, a row that is not
// exactly one physical line desynchronises the painter's cursor math.
func TestCoordinateRowIsOnePhysicalRow(t *testing.T) {
	for _, w := range []int{4, 8, 12, 60} {
		tr := coordFixture(t, true)
		tr.resize(w, 40)
		for i, r := range tr.lines() {
			if strings.ContainsAny(r, "\n\r\t") {
				t.Fatalf("w=%d row %d contains a control character: %q", w, i, r)
			}
			if !coordRowRE.MatchString(strings.TrimSpace(stripANSI(r))) {
				continue // only the address rows are this test's business
			}
			if got := runewidth.StringWidth(stripANSI(r)); got > w {
				t.Fatalf("w=%d row %d is %d columns wide: %q", w, i, got, r)
			}
		}
	}
}

// TestCoordinateBelongsToItsNodesSpan: the address carries the node's ref, so
// selecting the node covers the coordinate too, and nodeSpanOf: which
// ensureSelectionVisible converts to absolute lines: starts AT the address.
// If these two ever disagreed, scroll-into-view would land one row off per
// node under Ctrl-O.
func TestCoordinateBelongsToItsNodesSpan(t *testing.T) {
	tr := coordFixture(t, true)
	tr.buildIndex()
	ref := nodeRef{turn: 8, index: 1} // the tool
	span, ok := tr.nodeSpanOf(ref)
	if !ok {
		t.Fatal("no span for 8.1")
	}
	rows := tr.lines()
	first := strings.TrimSpace(stripANSI(rows[span.first]))
	if first != "8.1 · 01:25:47" {
		t.Fatalf("the node's span starts at %q, want its coordinate row", first)
	}
	// And it is genuinely selectable as part of the node: the span's first row
	// takes the selection cue when the node is focused.
	tr.selection = nodeSelection{active: true,
		anchor: selectionPoint{nodeRef: ref}, focus: selectionPoint{nodeRef: ref}}
	decorated := tr.lines()[span.first]
	if !strings.Contains(stripANSI(decorated), "▎") {
		t.Fatalf("the coordinate row took no selection cue: %q", decorated)
	}
}

// TestVerboseChromeStillHugsItsRule: the coordinate row sits BELOW the voice
// header, so the invariant TestVoiceHeaderHugsItsRule pins (a rule is the
// overline of the header beneath it, with nothing between) holds under Ctrl-O
// as well. It is asserted separately because that test runs verbose OFF, and
// a row inserted in the wrong place would have been invisible to it.
func TestVerboseChromeStillHugsItsRule(t *testing.T) {
	assertNoGapBelowRule(t, "transcript verbose", coordFixture(t, true).lines())

	// And the chrome around the question keeps its shape, with the address
	// between the header and the text rather than between the rule and the
	// header.
	rows := plainRows(coordFixture(t, true).lines())
	i := -1
	for k, r := range rows {
		if strings.Contains(r, "QUESTIONEIGHT") {
			i = k
			break
		}
	}
	if i < 3 {
		t.Fatalf("no question in:\n%s", strings.Join(rows, "\n"))
	}
	got := []string{rows[i-3], rows[i-2], rows[i-1], rows[i]}
	want := []string{"> input", "", "8.-1", "QUESTIONEIGHT"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("question chrome under verbose:\n got %q\nwant %q", got, want)
	}
}

// TestCoordinateLabelShapes pins the formatter directly, including the two
// edges: the inquiry's negative sentinel, and an unstamped node, which prints
// no time rather than 1970.
func TestCoordinateLabelShapes(t *testing.T) {
	at := time.Date(2026, 7, 27, 1, 23, 45, 0, time.Local).UnixMilli()
	cases := []struct {
		turn, node int
		at         int64
		want       string
	}{
		{12, 3, at, "12.3 · 01:23:45"},
		{12, inquiryNode, 0, "12.-1"},
		{1, 0, 0, "1.0"},
	}
	for _, c := range cases {
		if got := coordLabel(c.turn, c.node, c.at); got != c.want {
			t.Errorf("coordLabel(%d,%d,%d) = %q, want %q", c.turn, c.node, c.at, got, c.want)
		}
	}
}
