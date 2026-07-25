package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// contentParts counts parts that actually carry nodes, i.e. content rather
// than a bare live marker.
func contentParts(frames []Page) int {
	n := 0
	for _, f := range frames {
		for _, part := range f.Parts {
			if len(part.Nodes) > 0 {
				n++
			}
		}
	}
	return n
}

// TestAbandon_DropsOpenWithoutFolding: a partial streaming suffix that is
// abandoned must not fold into its turn (no duplication next round) and must
// broadcast nothing; a subsequent Read must not resurrect it.
func TestAbandon_DropsOpenWithoutFolding(t *testing.T) {
	s := NewServer()
	var frames []Page
	s.Subscribe(func(p Page) { frames = append(frames, p) })

	s.OpenTurn(1)
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "partial thinking"}})
	before := contentParts(frames)

	s.Abandon()
	if after := contentParts(frames); after != before {
		t.Fatalf("Abandon broadcast content (%d -> %d)", before, after)
	}

	// The turn exists but holds nothing, and nothing is live.
	r := s.Read(Anchor{}, 1<<20)
	for _, part := range r.Parts {
		if len(part.Nodes) > 0 {
			t.Fatalf("abandoned suffix leaked into Read: %+v", part)
		}
		if part.Live != nil {
			t.Fatalf("abandoned suffix leaked into Read as live: %+v", part.Live)
		}
	}

	// A fresh turn still folds and seals normally.
	s.OpenTurn(2)
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "real answer"}})
	s.Close()
	s.Seal(nil)

	r = s.Read(Anchor{}, 1<<20)
	var withNodes []TurnPart
	for _, part := range r.Parts {
		if len(part.Nodes) > 0 {
			withNodes = append(withNodes, part)
		}
	}
	if len(withNodes) != 1 || withNodes[0].ID != 2 {
		t.Fatalf("after abandon, expected only turn 2 to carry nodes, got %+v", withNodes)
	}
	if !withNodes[0].Sealed {
		t.Error("turn 2 should be sealed")
	}
}
