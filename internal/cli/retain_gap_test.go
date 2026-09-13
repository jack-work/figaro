package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// AFTER A RETAINED SWITCH THERE MUST BE NO HOLE AT THE SEAM. The prefix is
// held, the suffix arrives, and if the store cannot see that they touch, the
// pager draws a gap sentinel and fills it from the wire: the whole saving,
// spent twice, plus a row that says history is missing when it is not.
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
