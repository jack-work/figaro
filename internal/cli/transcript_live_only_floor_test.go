package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A PAGER HOLDING ONLY A LIVE TURN MUST NOT ANCHOR ITS READ AT THE ZERO
// ANCHOR, because on this wire the zero anchor MEANS the tail: the read that
// was supposed to fetch the turn's missing head fetches the tail it already
// has, changes nothing, and is asked for again on the next frame.
//
// It is reachable the moment a cold join meets a turn too big for one page:
// every node belongs to the open turn, so the client retains no CLOSED
// message, so TailFrom has nothing to answer with.
func TestResetToTailAnchorsOnTheOpenMessage(t *testing.T) {
	s := aria.NewServer()
	s.OpenInquiry(1, "the long one", nil)
	s.OpenTurn(1)
	nodes := make([]livedoc.Node, 12)
	for i := range nodes {
		nodes[i] = livedoc.Node{Type: livedoc.NodeProse,
			Markdown: fmt.Sprintf("node %02d: %s", i, strings.Repeat("x", 300))}
	}
	s.Update(nil, nodes, 0)

	client := aria.NewClient()
	page := s.ReadBefore(aria.Anchor{}, aria.Anchor{}, 1024)
	if !page.Parts[0].ClippedHead {
		t.Fatalf("fixture: the read was not clipped, so the pager is not short of history")
	}
	client.Apply(page, aria.Notify)
	client.SetMoreBefore(page.More.Before)
	if client.Count() != 0 {
		t.Fatalf("fixture: %d closed messages; the live-only case is not being tested", client.Count())
	}

	var out strings.Builder
	tr := newTranscript(&out, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.resetToTail()

	if tr.from == (aria.Anchor{}) {
		open := client.Open()
		t.Errorf("the window floor is the zero anchor, which this wire reads as the tail: "+
			"a read for older history would return the page we already hold. "+
			"The open message begins at {%d %d}", open.Turn, open.From)
	}
}
