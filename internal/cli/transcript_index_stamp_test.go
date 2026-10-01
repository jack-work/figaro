package cli

import (
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// THE INDEX IS ASKED ON EVERY FRAME AND BY THIRTY CALL SITES, so the only
// honest thing for it to do on a frame where nothing moved is nothing. These
// tests pin the three outcomes: no work, the live entry only, and the full
// walk.

// stampTranscript is a settled pager over sealed history, plus a live turn
// when live is true.
func stampTranscript(t *testing.T, live bool) (*transcript, *aria.Client) {
	t.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, 8)
	for i := range parts {
		parts[i] = aria.TurnPart{Turn: aria.Turn{
			ID: uint64(i + 1), Sealed: true,
			Inquiry: "a question that is long enough to wrap at least once in a hundred columns",
			Nodes:   []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "an answer"}},
		}}
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	if live {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 9, Inquiry: "the live question",
			Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning}},
		}}}}, aria.Notify)
	}
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "stamp", time.Unix(0, 0))
	tr.enter()
	tr.buildIndex()
	return tr, client
}

// trapRowCache empties the row cache WITHOUT bumping rowRev. Any walk of the
// window refills it, so a cache that is still empty afterwards proves the walk
// did not happen.
func trapRowCache(tr *transcript) {
	tr.rowCache = map[sliceKey]cachedMessage{}
}

func TestBuildIndexDoesNothingWhenNothingMoved(t *testing.T) {
	tr, _ := stampTranscript(t, false)
	total, entries := tr.index.total, len(tr.index.entries)
	trapRowCache(tr)

	for range 20 { // twenty clock ticks' worth of asking
		tr.buildIndex()
	}
	if n := len(tr.rowCache); n != 0 {
		t.Fatalf("buildIndex re-walked the window with nothing changed: %d messages re-rendered", n)
	}
	if tr.index.total != total || len(tr.index.entries) != entries {
		t.Fatalf("the index changed under a no-op rebuild: %d/%d rows, %d/%d entries",
			tr.index.total, total, len(tr.index.entries), entries)
	}
}

// settle is the frame path's entry, and it used to rebuild up to four times
// per frame on its own.
func TestSettleDoesNothingWhenNothingMoved(t *testing.T) {
	tr, _ := stampTranscript(t, false)
	tr.settle()
	trapRowCache(tr)
	tr.settle()
	if n := len(tr.rowCache); n != 0 {
		t.Fatalf("settle re-walked the window with nothing changed: %d messages re-rendered", n)
	}
}

// A SPINNER IS NOT A NEW WINDOW. While a tool runs, the clock must recompose
// the live message and nothing else.
func TestTickRefreshesOnlyTheLiveEntry(t *testing.T) {
	tr, _ := stampTranscript(t, true)
	last := tr.index.entries[len(tr.index.entries)-1]
	if !last.open {
		t.Fatal("the live message is not the last entry; the fixture is wrong")
	}
	trapRowCache(tr)
	tr.tick++
	tr.buildIndex()
	if n := len(tr.rowCache); n != 0 {
		t.Fatalf("a spinner tick re-walked the window: %d messages re-rendered", n)
	}
	if got := tr.index.entries[len(tr.index.entries)-1]; !got.open || got.start != last.start {
		t.Fatalf("the live entry moved on a tick: start %d, want %d", got.start, last.start)
	}
}

// And with nothing animating, the tick is not even that much.
func TestTickIsFreeWithNothingAnimating(t *testing.T) {
	tr, client := stampTranscript(t, true)
	// Seal the tool: nothing under the open message animates now.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 9, Inquiry: "the live question",
		Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: "done"}},
	}}}}, aria.Notify)
	tr.buildIndex()
	before := tr.index.stamp
	tr.tick += 7
	tr.buildIndex()
	if tr.index.stamp != before {
		t.Fatal("a tick dirtied the index with nothing animating under it")
	}
}

// Content, width and folds all still land.
func TestBuildIndexRebuildsWhenSomethingMoved(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(*transcript, *aria.Client)
	}{
		{"a message arrives", func(tr *transcript, c *aria.Client) {
			c.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
				ID: 20, Sealed: true, Inquiry: "another question",
				Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "another answer"}},
			}}}}, aria.Notify)
		}},
		{"the pane resizes", func(tr *transcript, c *aria.Client) { tr.setSize(72, 40) }},
		{"rows are dropped", func(tr *transcript, c *aria.Client) { tr.invalidateRows() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, client := stampTranscript(t, false)
			trapRowCache(tr)
			tc.move(tr, client)
			tr.buildIndex()
			if len(tr.rowCache) == 0 {
				t.Fatal("the index was not rebuilt after the window changed under it")
			}
		})
	}
}
