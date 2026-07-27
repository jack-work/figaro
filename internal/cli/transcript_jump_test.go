package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// jumpFixture is an aria whose turn ids START AT firstTurn — the fork case,
// where StampIDs adopts the parent's numbering and the first turn is emphatically
// not 1. Pass firstTurn=1 for the ordinary case.
func jumpFixture(tb testing.TB, firstTurn, turns int) *transcript {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
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
	client.Apply(aria.Page{Parts: parts})
	// The wire has not said the aria begins here, so the pager may still walk
	// backward — which is the interesting case for a FORK, whose first turn id
	// is not 1 and whose floor can only be found by an empty read.
	client.SetMoreBefore(true)
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, &ariaView{settings: &renderSettings{}},
		client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr
}

// typeJump drives the ':' box one byte at a time, through the real dispatcher,
// the way a human types. Whole-string feeds have certified byte-vs-rune bugs
// on this codebase before.
func typeJump(tr *transcript, text string) {
	tr.key(':')
	for i := 0; i < len(text); i++ {
		tr.key(text[i])
	}
	tr.key(0x0d)
}

// topRow is the first body row of the current viewport, stripped.
func topRow(tr *transcript) string {
	tr.settle()
	return plainBody(tr.lineAt(tr.offset))
}

// TestJumpSnapsToATurn: `:N` puts the head of turn N at the top of the
// viewport and selects its question, so the landing names itself.
func TestJumpSnapsToATurn(t *testing.T) {
	tr := jumpFixture(t, 1, 8)
	typeJump(tr, "3")
	if tr.inJump {
		t.Fatal("the box is still open after Enter")
	}
	if tr.follow {
		t.Fatal("a jump must detach from the live tail")
	}
	// The viewport's first rows are turn 3's chrome; the question follows.
	rows := viewportRows(tr, 8)
	if !containsRow(rows, "QUESTION3") {
		t.Fatalf(":3 did not put turn 3 at the top:\n%s", strings.Join(rows, "\n"))
	}
	if got := tr.selection.focus.nodeRef; got != (nodeRef{turn: 3, index: inquiryNode}) {
		t.Fatalf("selection landed on %+v, want turn 3's question", got)
	}
}

// TestJumpSnapsToANode: `:N.k` snaps to the node, not merely to the turn.
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

// TestJumpToTheInquirySentinel: the question is addressable exactly like a
// node, at the -1 the coordinate row prints.
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

// TestJumpZeroReachesTheFirstExistingTurn, on a normal aria AND on a fork
// whose first turn id is not 1.
//
// THE FORK CASE IS THE POINT. `:0` is not "turn 1": StampIDs adopts an
// already-stamped id, so a forked child continues its parent's numbering.
// Resolving `:0` by constructing an anchor would either miss (turn 1 does not
// exist there) or, worse, hand a backward read Anchor{Turn:0} — which means
// UNSET on the wire and returns the TAIL.
func TestJumpZeroReachesTheFirstExistingTurn(t *testing.T) {
	for _, first := range []int{1, 7} {
		t.Run(fmt.Sprintf("first-turn-%d", first), func(t *testing.T) {
			tr := jumpFixture(t, first, 8)
			// The whole aria is loaded, so the floor is known without paging
			// when it starts at 1; when it starts at 7 it is not, and the walk
			// has to discover it. Simulate the store's answer either way.
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

// drainJump plays the role of prefetchTranscriptPages for a fixture whose
// whole aria is already in the client: it answers every page request the pager
// makes with the empty page a store returns when there is nothing older. That
// is exactly the wire behaviour that latches the floor on a fork.
func drainJump(t *testing.T, tr *transcript) {
	t.Helper()
	for range jumpBudget + 4 {
		req, need := tr.pageCursor()
		if !need {
			return
		}
		tr.applyPage(req, historyPage{})
	}
	t.Fatal("the page cursor never stopped asking")
}

// TestJumpToAnUnreachableTargetReports: it must say so in the footer, not hang
// and not land somewhere else pretending it worked.
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
	if tr.offset != before || !tr.follow {
		t.Fatalf("a failed jump moved the reader: offset %d->%d follow %v",
			before, tr.offset, tr.follow)
	}
	if line, own := tr.jumpFooter(); !own || line != tr.jumpNote {
		t.Fatalf("the footer does not carry the note: %q %v", line, own)
	}
	// And the report is TRANSIENT: the next key gives the status row back.
	// A sticky note would eat the mantra/ctx/cost line for the whole session.
	tr.key('j')
	if tr.jumpNote != "" {
		t.Fatalf("the note survived a keystroke: %q", tr.jumpNote)
	}
	if _, own := tr.jumpFooter(); own {
		t.Fatal("the jump still owns the status row after a keystroke")
	}
}

// TestJumpBudgetTerminates: a store that keeps handing back pages which never
// reach the target must stop the walk, not spin.
func TestJumpBudgetTerminates(t *testing.T) {
	tr := jumpFixture(t, 100, 8)
	typeJump(tr, "3") // older than anything, and the floor never latches
	fetches := 0
	for range jumpBudget * 4 {
		req, need := tr.pageCursor()
		if !need {
			break
		}
		fetches++
		// A store that answers with the same content forever: no progress, no
		// floor. The budget is the only thing that can end this.
		tr.applyPage(req, historyPage{more: true, msgs: []aria.Message{{Turn: 100, Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "again"}}}}})
	}
	if tr.jump != nil {
		t.Fatalf("the walk never gave up after %d fetches", fetches)
	}
	if fetches > jumpBudget+1 {
		t.Fatalf("the walk spent %d fetches, budget is %d", fetches, jumpBudget)
	}
	if tr.jumpNote == "" {
		t.Fatal("giving up reported nothing")
	}
}

// TestJumpBoxTakesSlashAsText is the mirror of the rule the pager oracle pins
// for ':' inside the search box: each box's text is the other's trigger.
func TestJumpBoxTakesSlashAsText(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.key(':')
	for _, b := range []byte("1/2") {
		tr.key(b)
	}
	if tr.jumpQuery != "1/2" {
		t.Fatalf("jump box holds %q, want %q", tr.jumpQuery, "1/2")
	}
	if tr.inSearch {
		t.Fatal("'/' opened the search box from inside the jump box")
	}
	tr.key(0x1b) // Esc cancels the typing
	if tr.inJump || tr.jumpQuery != "" {
		t.Fatalf("Esc left the box open: inJump=%v q=%q", tr.inJump, tr.jumpQuery)
	}
}

// TestJumpParser covers the grammar, including the two edges that matter:
// turn 0 is a SENTINEL and never an address, and the inquiry's -1 is legal.
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

// TestGGStillMeansTopOfTheBuffer: the jump is an addition, not a replacement.
// `gg` remains the cheap gesture — top of what is HELD, no paging, no walk.
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

// plainBody is a rendered row reduced to its text: escapes gone, and the
// one-column selection gutter (a bar or a blank) off the front.
func plainBody(row string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stripANSI(row)), "▎"))
}

func containsRow(rows []string, want string) bool {
	for _, r := range rows {
		if r == want {
			return true
		}
	}
	return false
}
