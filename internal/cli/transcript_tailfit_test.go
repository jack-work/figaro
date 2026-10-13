package cli

import (
	"fmt"
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Following only adds history behind the tail, so a fall in the row total
// is the window arguing with itself.
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
		}}}}}, aria.Notify)
		tr.renderFrame()
		if tr.index.total < prev {
			drops++
			t.Logf("frame %2d: total fell %d -> %d (window %v)", f, prev, tr.index.total, tr.from)
		}
		prev = tr.index.total
	}
	return drops, tr
}

// Each case is one dump taller than any band a controller could have used.
func TestTailWindowHasAFixedPoint(t *testing.T) {
	for _, c := range []struct{ short, giant, depth int }{
		{2, 500, 11},
		{1, 600, 8},
		{2, 400, 11},
		{3, 300, 12},
		{4, 250, 14},
		{1, 2000, 20},
		{6, 120, 5},
	} {
		t.Run(fmt.Sprintf("short%d_giant%d_depth%d", c.short, c.giant, c.depth), func(t *testing.T) {
			drops, tr := streamOnto(t, spikeHistory(120, c.short, c.giant, c.depth), 24)
			if drops > 0 {
				t.Errorf("the row total fell %d times while following: no fixed point", drops)
			}
			// A window still being re-tuned on the last frame has not
			// settled.
			if !tr.tailTuned {
				t.Errorf("the tail window never latched (want %d messages)", tr.tailWant)
			}
		})
	}
}

// The cut is the largest suffix that fits the budget, from measured heights,
// never fewer than one message.
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

	_, tall := streamOnto(t, spikeHistory(3, 2, 4000, 1), 1)
	tall.buildIndex()
	if n, _ := tall.tailFit(budget); n < 1 {
		t.Fatal("a message taller than the budget emptied the window")
	}
}
