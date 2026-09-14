package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

// THE SEAM, MEASURED IN A TERMINAL.
//
// The pinned header is the block's own first two rows, so the row where it
// takes over and the row where it lets go must paint the same two lines. The
// shipped bug this pins: the header held the question's TEXT only, released
// while the heading was still above the pane, and a multi-line question spent
// its first rows of scroll with nobody's name on screen -- then the heading
// jumped back in. A unit test on the composed rows had certified both states
// as correct; the defect was that one followed the other.
func TestStickySeamPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives replay through tmux")
	}
	question := "REMINDER (figla) - clean-baseline rebuild: the worktree was dirty, " +
		"so the baseline is being rebuilt from commit 3a0e888, and this first " +
		"paragraph runs long enough to wrap several times.\n\n" +
		"--watch git -C /home/gluck/dev/spain-flake/master status -sb\n\n" +
		"Nothing to cancel: this reminder has already fired."
	n := livedoc.Node{Type: livedoc.NodeProse,
		Markdown: strings.Repeat("The answer runs on below the question.\n\n", 40) + "ANSWERLAST"}
	p := newAdornPaneFor(t, aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: question,
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: question}},
		Nodes:           []livedoc.Node{n},
	}}}}, "ANSWERLAST")
	p.key("C-n")
	p.key("Escape")
	p.key("g")
	p.key("g")

	top := p.rows()
	if !strings.HasPrefix(top[0], "> Gluck") || !strings.Contains(top[1], "REMINDER (figla)") {
		t.Fatalf("the question does not open the turn: %s", p.dump())
	}
	// Its own heading, and the first line under it with nothing between.
	want := strings.TrimRight(top[1], " ")

	for step := 1; step <= 6; step++ {
		p.key("j")
		rows := p.rows()
		if !strings.HasPrefix(rows[0], "∨ Gluck") {
			t.Fatalf("%d rows scrolled: the heading left the pane: %s", step, p.dump())
		}
		got := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(rows[1], " "), ".."), " ")
		if !strings.HasPrefix(want, got) {
			t.Fatalf("%d rows scrolled: pinned line %q is not the head of %q: %s", step, got, want, p.dump())
		}
	}
	t.Logf("scrolled into the answer:\n%s", p.capture(true))
}
