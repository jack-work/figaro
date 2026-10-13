package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func inquiryHistory() []aria.TurnPart {
	return []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "what did you find?", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first node"},
			{Type: livedoc.NodeProse, Markdown: "second node"},
		},
	}}}
}

// The question has a ref (inquiryNode): the walk stops on it, its rows carry
// the cue, and the copy path yields it.
func TestTranscriptInquiryIsSelectableAndCopyable(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	history := inquiryHistory()
	client.Apply(aria.Page{Parts: history}, aria.Notify)
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	tr.key(0x0e)
	want := nodeRef{turn: 1, index: inquiryNode}
	if !tr.selection.active || tr.selection.focus.nodeRef != want {
		t.Fatalf("first selection = %+v, want the inquiry %+v", tr.selection, want)
	}
	tr.render()
	rows := strings.Join(tr.lines(), "\n")
	for _, line := range strings.Split(rows, "\n") {
		if strings.Contains(stripANSI(line), "what did you find?") && !washed(line) {
			t.Fatalf("the selected question shows no selection cue:\n%s", rows)
		}
	}
	text, err := selectedTextForTest(tr, history)
	if err != nil || text != "what did you find?" {
		t.Fatalf("copied inquiry = %q, %v", text, err)
	}

	tr.selectNode(1, true)
	if got, err := selectedTextForTest(tr, history); err != nil ||
		got != "what did you find?\n\nfirst node" {
		t.Fatalf("extended selection = %q, %v", got, err)
	}
	if inquiryNode >= 0 {
		t.Fatalf("inquiryNode = %d must be negative to stay out of node space", inquiryNode)
	}
}

// The oldest retained slice can start mid-turn, so the backward fetch is
// anchored on (turn, node) or the head slice is behind a cursor no scroll reaches.
func TestTranscriptOlderFetchAnchorsOnTheNode(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 4, Inquiry: "the question", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "the tail"},
		}},
		From:        2,
		ClippedHead: true,
	}}}, aria.Notify)
	client.SetMoreBefore(true)
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.offset = 0

	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("no older-page request from a window that starts mid-turn")
	}
	if req.at.Turn != 4 || req.at.Node != 2 {
		t.Fatalf("request = (%d, %d), want turn 4 node 2: the oldest slice we hold",
			req.at.Turn, req.at.Node)
	}
}

// A window holding only the tail of turn 1 still has turn 1's head to fetch.
func TestTranscriptFirstTurnClippedStillPagesOlder(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 1, Inquiry: "the question", Sealed: true, Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "the tail"},
		}},
		From:        3,
		ClippedHead: true,
	}}}, aria.Notify)
	client.SetMoreBefore(true)
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.offset = 0

	req, ok := tr.pageCursor()
	if !ok || req.at.Node != 3 {
		t.Fatalf("request = %+v, ok = %v; turn 1's own head is still unread", req, ok)
	}
	if tr.atAriaFloor() {
		t.Fatal("called it the beginning while the first turn's head was unfetched")
	}
}

// A head slice lands above a tail slice of the same turn the reader is in,
// so the viewport anchor must name the slice, not the turn.
func TestTranscriptViewportAnchorSurvivesAHeadSliceLanding(t *testing.T) {
	// A viewport shorter than the window, or the offset is clamped.
	ft := ldrender.NewFakeTerminal(80, 10)
	client := aria.NewClient()
	var tail, headNodes []livedoc.Node
	for i := range 4 {
		tail = append(tail, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("tail node %d", i)})
	}
	// The head slice must not overlap the tail: the store clips a merged run
	// to its novel part.
	for i := range 2 {
		headNodes = append(headNodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("head node %d", i)})
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 5, Inquiry: "the question", Sealed: true, Nodes: tail},
		From:        2,
		ClippedHead: true,
	}}}, aria.Notify)
	tr := newTranscript(ft, 80, 10, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	tr.offset = 2

	head := aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 5, Inquiry: "the question", Sealed: true, Nodes: headNodes},
		ClippedTail: true,
	}}}
	if got := pageMessages(head); len(got) != 1 || got[0].From != 0 || got[0].Inquiry == "" {
		t.Fatalf("fixture: head slice = %+v", got)
	}
	tr.applyPage(transcriptPageRequest{at: aria.Anchor{Turn: 5, Node: 2}}, head)

	lines := tr.lines()
	lastHead := -1
	for i, l := range lines {
		if strings.Contains(stripANSI(l), "head node 1") {
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
