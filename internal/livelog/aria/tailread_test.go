package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// THE STATUS BAR MUST BE ABLE TO ASK FOR NOTHING. Metrics ride a page, so a
// client that wants only the figures asks for a page that cannot carry a
// message: forward, from past the last turn. If this read ever starts
// returning content, the idle cost of every pager goes back up by the size of
// whatever it returns (it was the whole last message, 3.7 KB, every 1.8s).
func TestServer_ForwardReadPastTheTailCarriesNothing(t *testing.T) {
	srv := NewServer()
	var turns []Turn
	for i := uint64(1); i <= 8; i++ {
		turns = append(turns, Turn{
			ID: i, Inquiry: "question", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "a long enough answer to notice"}},
		})
	}
	srv.Restore(turns)

	page := srv.Read(Anchor{Turn: ^uint64(0)}, 1)
	if len(page.Parts) != 0 {
		t.Fatalf("the probe read came back with %d parts; it must carry none", len(page.Parts))
	}

	// And the ordinary tail read still does, so the probe is not just a broken
	// server: the two differ by direction, not by the aria being empty.
	if tail := srv.ReadBefore(Anchor{}, Anchor{}, 1); len(tail.Parts) == 0 {
		t.Fatal("the tail read came back empty; the fixture holds nothing")
	}
}
