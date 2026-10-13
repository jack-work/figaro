package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

func bigNode(n int) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", n)}
}

func sealedTurnPage(id uint64, from uint64, nodes []livedoc.Node) aria.Page {
	return aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: id, Sealed: true, Nodes: nodes},
		From: from,
	}}}
}

// A page below the live suffix never receives a delta.
func TestPageMessages_RefetchIsIdentical(t *testing.T) {
	page := sealedTurnPage(3, 0, []livedoc.Node{bigNode(40000), bigNode(40000), bigNode(10)})
	a, err := json.Marshal(pageMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pageMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("re-fetching an immutable page produced different units")
	}
}

// A unit's From is its coordinate in the turn, not its index in the page.
func TestPageMessages_HonoursPartOffset(t *testing.T) {
	got := pageMessages(sealedTurnPage(9, 5, []livedoc.Node{bigNode(4), bigNode(4)}))
	if len(got) != 1 || got[0].From != 5 {
		t.Fatalf("unit From = %v, want 5, a clipped part starts where the wire says", got)
	}
}
