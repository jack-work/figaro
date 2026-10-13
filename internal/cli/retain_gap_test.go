package cli

import (
	"testing"

	"github.com/jack-work/figaro/sdk/aria"
)

// The held prefix and the arriving suffix must touch, or the pager draws a
// gap sentinel and refills from the wire.
func TestRetain_NoHoleAtTheSeam(t *testing.T) {
	old := aria.NewClient()
	old.Apply(aria.Page{Parts: partsFor(1, 40, "A")}, aria.Notify)
	c := old.CloneBelow(21)
	c.Apply(aria.Page{Parts: partsFor(21, 3, "B"), More: aria.More{Before: true}}, aria.Notify)

	top, ok := c.TailFrom(1)
	if !ok {
		t.Fatal("the store holds nothing")
	}
	segs := c.Query(aria.Anchor{Turn: 1}, top)
	if len(segs) != 1 || segs[0].Gap != nil {
		for _, s := range segs {
			if s.Gap != nil {
				t.Errorf("hole at %v..%v", s.Gap.From, s.Gap.To)
			}
		}
		t.Fatalf("the seam is broken: %d segments over the whole store", len(segs))
	}
	if !c.Contiguous(aria.Anchor{Turn: 1}) {
		t.Fatal("the store does not read as contiguous from its own floor")
	}
}
