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

// inquiryHistory is one turn whose question opens it and whose nodes follow.
func inquiryHistory() []aria.TurnPart {
	return []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "what did you find?", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first node"},
			{Type: livedoc.NodeProse, Markdown: "second node"},
		},
	}}}
}

// The question was a NODE once, and selectable for it. Made text on the turn,
// it kept its rows but lost its ref — so Ctrl-N walked straight past it and y
// could not copy it. It has one again (inquiryNode), and this pins the three
// things a ref buys: the walk stops on it, the rows carry the selection cue,
// and the copy path yields the question itself.
func TestTranscriptInquiryIsSelectableAndCopyable(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	history := inquiryHistory()
	client.Apply(aria.Page{Parts: history})
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	tr.key(0x0e) // Ctrl-N: the first selectable point of the turn
	want := nodeRef{turn: 1, index: inquiryNode}
	if !tr.selection.active || tr.selection.focus.nodeRef != want {
		t.Fatalf("first selection = %+v, want the inquiry %+v", tr.selection, want)
	}
	tr.render()
	rows := strings.Join(tr.lines(), "\n")
	for _, line := range strings.Split(rows, "\n") {
		if strings.Contains(stripANSI(line), "what did you find?") && !strings.Contains(line, "▎") {
			t.Fatalf("the selected question shows no selection cue:\n%s", rows)
		}
	}
	text, err := selectedTextForTest(tr, history)
	if err != nil || text != "what did you find?" {
		t.Fatalf("copied inquiry = %q, %v", text, err)
	}

	// And it is ordered ahead of the turn's nodes, which is where it is drawn.
	tr.selectNode(1, true)
	if got, err := selectedTextForTest(tr, history); err != nil ||
		got != "what did you find?\n\nfirst node" {
		t.Fatalf("extended selection = %q, %v", got, err)
	}
	// The sentinel cannot collide with a node: node indices are From+i >= 0.
	if inquiryNode >= 0 {
		t.Fatalf("inquiryNode = %d must be negative to stay out of node space", inquiryNode)
	}
}

// A turn too big for one page reaches the pager as slices, and the oldest one
// retained can start MID-TURN. The backward fetch is anchored on (turn, node)
// for exactly that case: anchored on the turn alone it asks for what precedes
// the WHOLE turn, so the head slice — the only one carrying the question — is
// behind a cursor no scroll can reach, and the question is gone for good.
func TestTranscriptOlderFetchAnchorsOnTheNode(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 4, Inquiry: "the question", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "the tail"},
		}},
		From:        2,
		ClippedHead: true,
	}}})
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.checkOlder = true

	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("no older-page request from a window that starts mid-turn")
	}
	if req.before != 4 || req.beforeNode != 2 {
		t.Fatalf("request = (%d, %d), want turn 4 node 2 — the oldest slice we hold",
			req.before, req.beforeNode)
	}
}

// The same rule at the very first turn: a window holding only the tail of turn
// 1 still has history to fetch (turn 1's own head), so the "nothing older"
// latch must not fire on the turn id alone.
func TestTranscriptFirstTurnClippedStillPagesOlder(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 1, Inquiry: "the question", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "the tail"},
		}},
		From:        3,
		ClippedHead: true,
	}}})
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.checkOlder = true

	req, ok := tr.pageCursor()
	if !ok || req.beforeNode != 3 {
		t.Fatalf("request = %+v, ok = %v; turn 1's own head is still unread", req, ok)
	}
	if tr.noMoreOlder {
		t.Fatal("latched 'no more older' while the first turn's head was unfetched")
	}
}

// When the head slice finally lands, it lands ABOVE a tail slice of the SAME
// turn that the reader is already looking at. The viewport anchor therefore has
// to name the SLICE: named by turn alone it restores to the first line of the
// turn, which snaps the reader to the top of the block they were inside — the
// jump that a 'k' promotion into a clipped turn produced.
func TestTranscriptViewportAnchorSurvivesAHeadSliceLanding(t *testing.T) {
	// A viewport far shorter than the window, or the offset is clamped and the
	// anchor never gets a chance to be wrong.
	ft := ldrender.NewFakeTerminal(80, 10)
	client := aria.NewClient()
	var tail, headNodes []livedoc.Node
	for i := range 4 {
		tail = append(tail, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("tail node %d", i)})
		headNodes = append(headNodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("head node %d", i)})
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 5, Inquiry: "the question", Sealed: true, Nodes: tail},
		From:        2,
		ClippedHead: true,
	}}})
	tr := newTranscript(ft, 80, 10, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	tr.offset = 2

	head := committedMessages(aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 5, Inquiry: "the question", Sealed: true, Nodes: headNodes},
		ClippedTail: true,
	}}})
	if len(head) != 1 || head[0].From != 0 || head[0].Inquiry == "" {
		t.Fatalf("fixture: head slice = %+v", head)
	}
	tr.applyPage(transcriptPageRequest{before: 5, beforeNode: 2, direction: pageOlder}, head)

	// The landing shifts the tail slice down by the separator it now needs;
	// what must NOT happen is the viewport ending up ABOVE the newly prepended
	// head, which is what a turn-granular anchor does — it restores to the
	// turn's first line, i.e. the top of the block the reader was inside.
	lines := tr.lines()
	lastHead := -1
	for i, l := range lines {
		if strings.Contains(stripANSI(l), "head node 3") {
			lastHead = i
		}
	}
	if lastHead < 0 {
		t.Fatalf("fixture: the head slice never rendered:\n%s", strings.Join(lines, "\n"))
	}
	if tr.offset <= lastHead {
		t.Fatalf("the page landing moved the viewport to line %d, above the head's last line %d",
			tr.offset, lastHead)
	}
}
