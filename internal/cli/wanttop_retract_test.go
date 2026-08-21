package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// wantTop's contract is "any deliberate move elsewhere retracts it"
// (transcript.go). scroll, landJump and resetToTail honour that; these probe
// the two deliberate moves that write t.offset WITHOUT going through them,
// a search landing and a selection reveal, with a page already in flight,
// which is exactly when a stale intent yanks the reader back to line 0.

func wantTopFixture(t *testing.T) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	msg := func(i int) aria.TurnPart {
		return aria.TurnPart{Turn: aria.Turn{ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}}
	}
	for i := 5; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{msg(i)}})
	}
	client.SetMoreBefore(true)
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('g')
	tr.key('g') // arm the standing Home; history exists below, so it stands
	if !tr.wantTop {
		t.Fatal("fixture: gg over history should arm wantTop")
	}
	return tr, ft
}

func olderPage() historyPage {
	parts := make([]aria.TurnPart, 0, 4)
	for i := 1; i <= 4; i++ {
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}})
	}
	return committedPage(aria.Page{Parts: parts})
}

// A search that lands in-window is a deliberate move: the fetch gg armed must
// not drag the reader back to the top when it lands.
func TestWantTop_SearchLandingRetractsIt(t *testing.T) {
	tr, ft := wantTopFixture(t)
	req, ok := tr.pageCursor() // the fetch gg armed, now in flight
	if !ok {
		t.Fatal("fixture: gg should arm a backward fetch")
	}
	tr.find("msg07")
	if tr.wantTop {
		t.Fatal("a search landing is a deliberate move; wantTop should retract")
	}
	tr.applyPage(req, olderPage())
	if screen := strings.Join(ft.Screen(), "\n"); !strings.Contains(screen, "msg07") {
		t.Fatalf("the landing yanked the reader off their match:\n%s", screen)
	}
}

// Entering a selection is a deliberate move too, the reader is reading nodes,
// not still asking for the beginning.
func TestWantTop_SelectionRetractsIt(t *testing.T) {
	tr, _ := wantTopFixture(t)
	tr.selectNode(1, false)
	if tr.wantTop {
		t.Fatal("a selection move is a deliberate move; wantTop should retract")
	}
}
