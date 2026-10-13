package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A forward read from past the last turn carries metrics and no content;
// the status bar polls it.
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

	if tail := srv.ReadBefore(Anchor{}, Anchor{}, 1); len(tail.Parts) == 0 {
		t.Fatal("the tail read came back empty; the fixture holds nothing")
	}
}
