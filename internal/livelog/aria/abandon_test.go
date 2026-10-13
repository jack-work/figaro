package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

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

func TestAbandon_DropsOpenWithoutFolding(t *testing.T) {
	s := NewServer()
	var frames []Page
	s.Subscribe(func(p Page) { frames = append(frames, p) })

	s.OpenTurn(1)
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "partial thinking"}}, 0)
	before := contentParts(frames)

	s.Abandon()
	if after := contentParts(frames); after != before {
		t.Fatalf("Abandon broadcast content (%d -> %d)", before, after)
	}

	r := s.Read(Anchor{}, 1<<20)
	for _, part := range r.Parts {
		if len(part.Nodes) > 0 {
			t.Fatalf("abandoned suffix leaked into Read: %+v", part)
		}
		if part.Live != nil {
			t.Fatalf("abandoned suffix leaked into Read as live: %+v", part.Live)
		}
	}

	s.OpenTurn(2)
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "real answer"}}, 0)
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
