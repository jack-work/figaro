package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func headPager(t testing.TB, h int) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("QLINE%02d of a question too long for any header", i))
	}
	question := strings.Join(lines, "\n\n")
	var nodes []livedoc.Node
	for n := range 40 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d", n)})
	}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: question,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: question}},
		Nodes:           nodes,
	}}}}, aria.Notify)
	ft := ldrender.NewFakeTerminal(64, h)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ft, 64, h, view, client, "head0001", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	tr.offset = len(tr.stickyBlockOf(1).rows) + 4
	tr.buildIndex()
	return tr, ft
}

func painted(tr *transcript, ft *ldrender.FakeTerminal) []string {
	tr.renderFrame()
	return plain(ft.Screen())
}

func TestHead_ExpandsToAQuarterOfThePane(t *testing.T) {
	tr, ft := headPager(t, 40)
	tr.key('p')
	if !tr.headFocused() || tr.mode() != modeList {
		t.Fatalf("p did not expand and focus the header (mode %v)", tr.mode())
	}
	screen := painted(tr, ft)
	if !strings.HasPrefix(screen[0], headOpenGlyph+" Duke") {
		t.Fatalf("the expanded heading is %q", screen[0])
	}
	rule := 40/headPaneShare - 1
	if !strings.HasPrefix(screen[rule], ruleDouble) || !strings.HasSuffix(screen[rule], "1–8/59") {
		t.Fatalf("row %d is not the header's edge with its page: %q\n%s", rule, screen[rule], strings.Join(screen, "\n"))
	}
	if !strings.Contains(screen[1], "QLINE00") {
		t.Fatalf("the question does not start under its heading: %q", screen[1])
	}
}

func TestHead_PagesWithTheListMotions(t *testing.T) {
	tr, ft := headPager(t, 40)
	tr.key('p')
	tr.key('j')
	tr.key('j')
	if screen := painted(tr, ft); !strings.HasSuffix(screen[9], "3–10/59") {
		t.Fatalf("j did not page the header: %q", screen[9])
	}
	tr.key('G')
	if screen := painted(tr, ft); !strings.HasSuffix(screen[9], "52–59/59") || !strings.Contains(screen[8], "QLINE29") {
		t.Fatalf("G did not reach the end of the question:\n%s", strings.Join(screen[:10], "\n"))
	}
	tr.key('u')
	tr.key('g')
	tr.key('g')
	if screen := painted(tr, ft); !strings.HasSuffix(screen[9], "1–8/59") {
		t.Fatalf("gg did not return to the top: %q", screen[9])
	}
}

// Focused, the header floats over the body like the collapsed one; with T the
// body is laid out under it and nothing is hidden.
func TestHead_TMakesItARegion(t *testing.T) {
	tr, ft := headPager(t, 40)
	floated, _ := tr.layoutNow()
	tr.key('p')
	if got, _ := tr.layoutNow(); got != floated {
		t.Fatalf("the focused header shortened the body: %d, want %d", got, floated)
	}
	first := plain(tr.window(tr.offset, tr.offset+1, nil))[0]
	tr.key('T')
	if tr.headFocused() || !tr.head.open {
		t.Fatal("T closed the header or kept its focus")
	}
	body, _ := tr.layoutNow()
	if body != floated-10 {
		t.Fatalf("the body is %d rows under a 10-row header, want %d", body, floated-10)
	}
	screen := painted(tr, ft)
	if screen[10] != first {
		t.Fatalf("the body does not start under the header: row 10 is %q, want %q", screen[10], first)
	}
	if tr.headRows() != 0 {
		t.Fatalf("a region covers %d body rows", tr.headRows())
	}
	tr.key('j')
	if tr.offset == 0 || !tr.head.open {
		t.Fatal("j with the transcript focused did not scroll the body under the header")
	}
	tr.key('T')
	if !tr.headFocused() {
		t.Fatal("T did not hand the keys back to the header")
	}
}

func TestHead_FullscreenTakesThePane(t *testing.T) {
	tr, ft := headPager(t, 40)
	tr.key('p')
	tr.key('F')
	screen := painted(tr, ft)
	edge := -1
	for i, r := range screen {
		if strings.HasPrefix(r, ruleDouble) {
			edge = i
		}
	}
	if edge < 30 {
		t.Fatalf("fullscreen header ends at row %d of 40:\n%s", edge, strings.Join(screen, "\n"))
	}
	tr.key('T')
	if tr.fullHead() || tr.headPush() != 10 {
		t.Fatalf("with T the fullscreen header did not recede to its quarter (push %d)", tr.headPush())
	}
}

func TestHead_EscFoldsItBack(t *testing.T) {
	tr, ft := headPager(t, 40)
	tr.key('p')
	tr.key(0x1b)
	if tr.head.open || tr.mode() != modeTranscript {
		t.Fatalf("Esc left the header open (mode %v)", tr.mode())
	}
	screen := painted(tr, ft)
	if !strings.HasPrefix(screen[0], headShutGlyph) || !strings.HasPrefix(screen[2], ruleDouble) {
		t.Fatalf("the header did not return to its two rows:\n%s", strings.Join(screen[:4], "\n"))
	}
	tr.key('p')
	tr.key('p')
	if tr.head.open {
		t.Fatal("p did not fold the header it expanded")
	}
}

func TestHead_AnotherKeyFoldsItAndActs(t *testing.T) {
	tr, _ := headPager(t, 40)
	tr.key('p')
	tr.key('v')
	if tr.head.open || !tr.visual.active() {
		t.Fatalf("v from the header: open=%v visual=%v", tr.head.open, tr.visual.active())
	}
}
