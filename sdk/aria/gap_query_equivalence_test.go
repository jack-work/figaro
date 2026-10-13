package aria

import (
	"github.com/jack-work/figaro/api/livedoc"
	"testing"
)

func TestGapIteratorHonorsFirstTurnLikeQuery(t *testing.T) {
	c := NewClient()
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 2, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "two"}}}}}}, Notify)
	c.SetMoreBefore(true)
	var queried, iterated []Gap
	for _, s := range c.Query(Anchor{}, Anchor{Turn: 2}) {
		if s.Gap != nil {
			queried = append(queried, *s.Gap)
		}
	}
	c.ForEachSegment(Anchor{}, Anchor{Turn: 2}, func(Message) bool { return true }, func(g Gap) bool { iterated = append(iterated, g); return true })
	if len(queried) != 1 || len(iterated) != 1 || queried[0] != iterated[0] {
		t.Fatalf("gap query=%v iterator=%v", queried, iterated)
	}
}
