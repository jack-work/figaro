package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// TestTailWindowHasAFixedPoint is the unit-level companion to the spike tape.
//
// The tape proves the defect is gone for one recorded shape; this proves the
// PROPERTY for a family of them, which is what a fixed-point claim actually
// asserts. Each case is short messages with one dump taller than any hysteresis
// band a controller could have used, at a different depth and a different
// height, while a turn streams.
//
// The invariant is the one the footer reads: while following, the retained row
// total may only grow. It fell and rose on every frame before tailFit, because
// tuneTail sized the window from the average height of the window that sizing
// produced.
func TestTailWindowHasAFixedPoint(t *testing.T) {
	type shape struct{ short, giant, depth int }
	for _, c := range []shape{
		{2, 500, 11},  // the shape the tape was minted from
		{1, 600, 8},   // taller dump, closer to the tail
		{2, 400, 11},  // just over the old shrink threshold
		{3, 300, 12},  // inside the old band
		{4, 250, 14},  // under it
		{1, 2000, 20}, // a dump far taller than the whole budget
		{6, 120, 5},   // several messages of similar size, no single spike
	} {
		t.Run(fmt.Sprintf("short%d_giant%d_depth%d", c.short, c.giant, c.depth), func(t *testing.T) {
			client := aria.NewClient()
			client.SetClosedLimit(1000)
			applyTail(client, readBefore(spikeHistory(120, c.short, c.giant, c.depth), recentCursor, 120))
			tr := newTranscript(ldrender.NewFakeTerminal(100, 33), 100, 33,
				ldrender.NodeText{}, client, "aria", time.Time{})
			tr.enter()

			stream, prev, drops := "", 0, 0
			for f := range 24 {
				stream += "thinking line " + itoa(f) + "\n\n"
				client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 121, Live: &aria.Live{
					From: 0, V: f,
					Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": stream}}},
				}}}}})
				tr.renderFrame()
				if tr.index.total < prev {
					drops++
					t.Logf("frame %2d: total fell %d -> %d (window %v)", f, prev, tr.index.total, tr.from)
				}
				prev = tr.index.total
			}
			if drops > 0 {
				t.Errorf("the row total fell %d times while following: no fixed point", drops)
			}
			// And it must SETTLE, not merely stop falling: a window still being
			// re-tuned on the last frame is one frame away from the same bug.
			if !tr.tailTuned {
				t.Errorf("the tail window never latched (want %d messages)", tr.tailWant)
			}
		})
	}
}

// TestTailFitCutsOnMeasuredHeights pins the mechanism itself: the cut is the
// largest suffix of the window that fits the budget, computed from the heights
// the index actually holds — never from an average, and never fewer than one
// message however tall that message is.
func TestTailFitCutsOnMeasuredHeights(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(1000)
	applyTail(client, readBefore(spikeHistory(60, 2, 500, 6), recentCursor, 60))
	tr := newTranscript(ldrender.NewFakeTerminal(100, 33), 100, 33,
		ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()
	tr.buildIndex()

	budget := pageRowBudget()
	fits, rows := tr.tailFit(budget)
	if fits < 1 {
		t.Fatal("tailFit kept no messages")
	}
	if rows > budget && fits > 1 {
		t.Fatalf("tailFit kept %d messages for %d rows, over budget %d", fits, rows, budget)
	}
	// Idempotent by construction: the same index must give the same cut.
	if again, _ := tr.tailFit(budget); again != fits {
		t.Fatalf("tailFit is not a function of the window: %d then %d", fits, again)
	}
	// A single message taller than the whole budget is still kept — the
	// alternative is a window with nothing in it.
	one := aria.NewClient()
	one.SetClosedLimit(10)
	applyTail(one, readBefore(spikeHistory(3, 2, 4000, 1), recentCursor, 3))
	tr2 := newTranscript(ldrender.NewFakeTerminal(100, 33), 100, 33,
		ldrender.NodeText{}, one, "aria", time.Time{})
	tr2.enter()
	tr2.buildIndex()
	if n, _ := tr2.tailFit(budget); n < 1 {
		t.Fatal("a message taller than the budget emptied the window")
	}
}
