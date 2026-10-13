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

// A search landing and a selection reveal write t.offset without going
// through scroll, and must still retract wantTop.

func wantTopFixture(t *testing.T) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	msg := func(i int) aria.TurnPart {
		return aria.TurnPart{Turn: aria.Turn{ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}}
	}
	for i := 5; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{msg(i)}}, aria.Notify)
	}
	client.SetMoreBefore(true)
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('g')
	tr.key('g')
	if !tr.wantTop {
		t.Fatal("fixture: gg over history should arm wantTop")
	}
	return tr, ft
}

func olderPage() aria.Page {
	parts := make([]aria.TurnPart, 0, 4)
	for i := 1; i <= 4; i++ {
		parts = append(parts, aria.TurnPart{Turn: aria.Turn{ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d", i)}}}})
	}
	return aria.Page{Parts: parts}
}

func TestWantTop_SearchLandingRetractsIt(t *testing.T) {
	tr, ft := wantTopFixture(t)
	req, ok := tr.pageCursor()
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

func TestWantTop_SelectionRetractsIt(t *testing.T) {
	tr, _ := wantTopFixture(t)
	tr.selectNode(1, false)
	if tr.wantTop {
		t.Fatal("a selection move is a deliberate move; wantTop should retract")
	}
}
