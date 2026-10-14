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

func jumpPage(firstTurn, turns int) aria.Page {
	parts := make([]aria.TurnPart, 0, turns)
	for i := range turns {
		id := uint64(firstTurn + i)
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{
			ID: id, Inquiry: fmt.Sprintf("QUESTION%d", id), Sealed: true,
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-0", id)},
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-1", id)},
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d-2", id)},
			},
		}})
	}
	return aria.Page{Parts: parts}
}

func jumpFixture(tb testing.TB, firstTurn, turns int) *transcript {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(jumpPage(firstTurn, turns), aria.Notify)
	// A fork's first turn id is not 1 and its floor is found only by an empty read.
	client.SetMoreBefore(true)
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr
}

// typeJump types one byte at a time through the real dispatcher.
func typeJump(tr *transcript, text string) {
	tr.key(':')
	for i := 0; i < len(text); i++ {
		tr.key(text[i])
	}
	tr.key(0x0d)
}

func topRow(tr *transcript) string {
	tr.settle()
	return plainBody(tr.lineAt(tr.offset))
}

func TestJumpSnapsToATurn(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	typeJump(tr, "3")
	if tr.boxOpen() {
		t.Fatal("the box is still open after Enter")
	}
	if tr.follow {
		t.Fatal("a jump must detach from the live tail")
	}
	rows := viewportRows(tr, 8)
	if !containsRow(rows, "QUESTION3") {
		t.Fatalf(":3 did not put turn 3 at the top:\n%s", strings.Join(rows, "\n"))
	}
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 3, index: inquiryNode}) {
		t.Fatalf("selection landed on %+v, want turn 3's question", got)
	}
}

func TestJumpSnapsToANode(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	typeJump(tr, "3.2")
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 3, index: 2}) {
		t.Fatalf("selection landed on %+v, want 3.2", got)
	}
	if got := topRow(tr); got != "NODE3-2" {
		t.Fatalf("the viewport's first row is %q, want NODE3-2", got)
	}
}

func TestJumpToTheInquirySentinel(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	typeJump(tr, "5.-1")
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 5, index: inquiryNode}) {
		t.Fatalf("selection landed on %+v, want turn 5's question", got)
	}
	if got := topRow(tr); got != "QUESTION5" {
		t.Fatalf("the viewport's first row is %q, want QUESTION5", got)
	}
}

// A forked child continues its parent's numbering, and Anchor{Turn:0} means
// unset on the wire, so `:0` cannot be resolved by constructing an anchor.
func TestJumpZeroReachesTheFirstExistingTurn(t *testing.T) {
	for _, first := range []int{1, 7} {
		t.Run(fmt.Sprintf("first-turn-%d", first), func(t *testing.T) {
			tr := jumpFixture(t, first, 8)
			typeJump(tr, "0")
			drainJump(t, tr)
			if tr.jump != nil {
				t.Fatalf(":0 never resolved (note %q)", tr.jumpNote)
			}
			if tr.jumpNote != "" {
				t.Fatalf(":0 reported %q", tr.jumpNote)
			}
			want := fmt.Sprintf("QUESTION%d", first)
			if rows := viewportRows(tr, 8); !containsRow(rows, want) {
				t.Fatalf(":0 landed at %q, want the first turn (%s):\n%s",
					topRow(tr), want, strings.Join(rows, "\n"))
			}
			if got := tr.selection.focus.turn; got != first {
				t.Fatalf(":0 selected turn %d, want %d", got, first)
			}
		})
	}
}

// drainJump answers every page request with the empty page that latches the floor.
func drainJump(t *testing.T, tr *transcript) {
	t.Helper()
	for range 32 {
		req, need := tr.pageCursor()
		if !need {
			return
		}
		tr.applyPage(req, aria.Page{})
	}
	t.Fatal("the page cursor never stopped asking")
}

func TestJumpToAnUnreachableTargetReports(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	before := tr.offset
	typeJump(tr, "999")
	drainJump(t, tr)
	if tr.jump != nil {
		t.Fatal("the walk is still running")
	}
	if tr.jumpNote == "" {
		t.Fatal("an unreachable target reported nothing")
	}
	if !strings.Contains(tr.jumpNote, "999") {
		t.Fatalf("the note does not name the target: %q", tr.jumpNote)
	}
	// The failure is a note on the bar now, which costs the body no rows, so
	// the reader must be exactly where it was. (It used to open a two-row
	// drawer, and this compared against the tail instead.)
	if !tr.follow {
		t.Fatalf("a failed jump detached the reader from the tail (offset %d->%d)", before, tr.offset)
	}
	if tr.offset != before {
		t.Fatalf("a failed jump moved the reader: offset %d -> %d", before, tr.offset)
	}
	if tr.showing(pitNote) {
		t.Fatal("a one-line failure opened a row of its own")
	}
	if got := tr.status.noticeText(); !strings.Contains(got, "999") {
		t.Fatalf("the failure is not on the bar: %q", got)
	}
	// The report is transient: the next key closes the drawer.
	tr.key('j')
	if tr.jumpNote != "" {
		t.Fatalf("the note survived a keystroke: %q", tr.jumpNote)
	}
	if _, own := tr.jumpFooter(); own {
		t.Fatal("the jump still owns the status row after a keystroke")
	}
}

// A page that does not hold the target is all the daemon can say; asking
// again is no progress.
func TestJumpAsksOnceAndStops(t *testing.T) {
	tr := jumpFixture(t, 100, 8)
	typeJump(tr, "3")
	seeks := 0
	for range 32 {
		req, need := tr.pageCursor()
		if !need {
			break
		}
		if req.seek == seekForward {
			seeks++
		}
		tr.applyPage(req, aria.Page{More: aria.More{Before: true}, Parts: []aria.TurnPart{{
			Turn: aria.Turn{ID: 100, Sealed: true,
				Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "again"}}}}}})
	}
	if tr.jump != nil {
		t.Fatalf("the jump is still standing after %d seeks", seeks)
	}
	if seeks > 1 {
		t.Fatalf("the jump read %d times for one coordinate", seeks)
	}
	if tr.jumpNote == "" {
		t.Fatal("giving up reported nothing")
	}
}

func TestJumpBoxTakesSlashAsText(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.key(':')
	for _, b := range []byte("1/2") {
		tr.key(b)
	}
	if tr.cmdline.String() != "1/2" {
		t.Fatalf("jump box holds %q, want %q", tr.cmdline.String(), "1/2")
	}
	if tr.inSearch {
		t.Fatal("'/' opened the search box from inside the jump box")
	}
	tr.key(0x1b)
	if tr.boxOpen() || tr.cmdline.String() != "" {
		t.Fatalf("Esc left the box open: inJump=%v q=%q", tr.boxOpen(), tr.cmdline.String())
	}
}

// Turn 0 is a sentinel, never an address; the inquiry's -1 is legal.
func TestJumpParser(t *testing.T) {
	ok := []struct {
		in   string
		want jumpTarget
	}{
		{"12", jumpTarget{turn: 12}},
		{" 12 ", jumpTarget{turn: 12}},
		{"12.3", jumpTarget{turn: 12, node: 3, hasNode: true}},
		{"12.0", jumpTarget{turn: 12, node: 0, hasNode: true}},
		{"12.-1", jumpTarget{turn: 12, node: inquiryNode, hasNode: true}},
		{"0", jumpTarget{start: true}},
	}
	for _, c := range ok {
		got, err := parseJumpTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("parse(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "  ", "abc", "-3", "12.x", "12.-2", "0.1", "1.2.3"} {
		if got, err := parseJumpTarget(bad); err == nil {
			t.Errorf("parse(%q) = %+v, want an error", bad, got)
		}
	}
}

// gg is top of what is held: no paging, no walk.
func TestGGStillMeansTopOfTheBuffer(t *testing.T) {
	tr := jumpFixture(t, 5, 8)
	tr.key('g')
	tr.key('g')
	if tr.offset != 0 {
		t.Fatalf("gg left the offset at %d, want 0", tr.offset)
	}
	if tr.jump != nil || tr.jumpNote != "" {
		t.Fatalf("gg started a jump: %v %q", tr.jump, tr.jumpNote)
	}
	if _, ok := tr.pageCursor(); !ok && !tr.atAriaFloor() {
		t.Fatal("gg no longer brings the viewport inside the prefetch distance")
	}
}

func viewportRows(tr *transcript, n int) []string {
	tr.settle()
	out := make([]string, 0, n)
	for i := tr.offset; i < tr.offset+n && i < tr.index.total; i++ {
		out = append(out, plainBody(tr.lineAt(i)))
	}
	return out
}

func plainBody(row string) string {
	return strings.TrimSpace(stripANSI(row))
}

func containsRow(rows []string, want string) bool {
	for _, r := range rows {
		if r == want {
			return true
		}
	}
	return false
}
