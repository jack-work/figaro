package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// streamOnto opens a pager over history, streams n live frames, and reports
// how many times the row total FELL: following only adds history behind the
// tail, so a fall is the window arguing with itself.
func streamOnto(t *testing.T, history []aria.TurnPart, n int) (drops int, tr *transcript) {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(1000)
	applyTail(client, readBefore(history, recentCursor, len(history)))
	tr = newTranscript(ldrender.NewFakeTerminal(100, 33), 100, 33,
		ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()
	stream, prev := "", 0
	for f := range n {
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
	return drops, tr
}

// The property behind the spike tape: the tape proves one shape, this the
// family. Each case is one dump taller than any band a controller could have
// used, at a different depth and height, while a turn streams.
func TestTailWindowHasAFixedPoint(t *testing.T) {
	for _, c := range []struct{ short, giant, depth int }{
		{2, 500, 11},  // the shape the tape was minted from
		{1, 600, 8},   // taller dump, closer to the tail
		{2, 400, 11},  // just over the old shrink threshold
		{3, 300, 12},  // inside the old band
		{4, 250, 14},  // under it
		{1, 2000, 20}, // a dump taller than the whole budget
		{6, 120, 5},   // several similar messages, no single spike
	} {
		t.Run(fmt.Sprintf("short%d_giant%d_depth%d", c.short, c.giant, c.depth), func(t *testing.T) {
			drops, tr := streamOnto(t, spikeHistory(120, c.short, c.giant, c.depth), 24)
			if drops > 0 {
				t.Errorf("the row total fell %d times while following: no fixed point", drops)
			}
			// It must SETTLE, not merely stop falling: a window still being
			// re-tuned on the last frame is one frame from the same bug.
			if !tr.tailTuned {
				t.Errorf("the tail window never latched (want %d messages)", tr.tailWant)
			}
		})
	}
}

// The mechanism: the cut is the largest suffix of the window that fits the
// budget, from heights the index holds: never an average, never fewer than
// one message however tall.
func TestTailFitCutsOnMeasuredHeights(t *testing.T) {
	budget := pageRowBudget()
	_, tr := streamOnto(t, spikeHistory(60, 2, 500, 6), 1)
	tr.buildIndex()

	fits, rows := tr.tailFit(budget)
	switch {
	case fits < 1:
		t.Fatal("tailFit kept no messages")
	case rows > budget && fits > 1:
		t.Fatalf("kept %d messages for %d rows, over budget %d", fits, rows, budget)
	}
	if again, _ := tr.tailFit(budget); again != fits {
		t.Fatalf("tailFit is not a function of the window: %d then %d", fits, again)
	}

	// A single message taller than the whole budget is still kept: the
	// alternative is a window with nothing in it.
	_, tall := streamOnto(t, spikeHistory(3, 2, 4000, 1), 1)
	tall.buildIndex()
	if n, _ := tall.tailFit(budget); n < 1 {
		t.Fatal("a message taller than the budget emptied the window")
	}
}
