package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// One frame's node count is proportional to the whole open region.
func TestOpenRegionGrowsWithTheRegion(t *testing.T) {
	sp, ss, _ := openRegionAgent(4, 200, false).composeTurn(nil)
	smallNodes := append(append([]livedoc.Node(nil), sp...), ss...)
	lp, ls, _ := openRegionAgent(64, 200, false).composeTurn(nil)
	largeNodes := append(append([]livedoc.Node(nil), lp...), ls...)
	small, large := len(smallNodes), len(largeNodes)
	if large <= small {
		t.Fatalf("open region of 64 rounds composed %d nodes, 4 rounds composed %d", large, small)
	}
	if want := small * 16; large != want {
		t.Fatalf("16x the rounds composed %d nodes, want %d: the frame is not linear in the region", large, want)
	}
}
