package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// countingClientView counts how many times the transcript re-derives its page
// window from the client, by counting the full View() snapshots resetToTail
// takes. A live frame must take none once the window is up to date.
func transcriptTailRev(tr *transcript) uint64 { return tr.tailRev }

func TestTranscriptFollowFrameDoesNotRebuildWindow(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
	ft := ldrender.NewFakeTerminal(50, 10)
	tr := newTranscript(ft, 50, 10, ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()

	rev := transcriptTailRev(tr)
	if rev == 0 {
		t.Fatal("entering the pager should snapshot the tail")
	}
	pages := &tr.pages[0]
	for range 20 {
		tr.render() // live frames: spinner ticks, open-message tokens
	}
	if got := transcriptTailRev(tr); got != rev {
		t.Fatalf("live frames rebuilt the window: rev %d -> %d", rev, got)
	}
	if &tr.pages[0] != pages {
		t.Fatal("live frames reallocated the page window")
	}

	// A newly committed message must still refresh the window.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: uint64(41), Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "message-041"}},
	}}}})
	tr.render()
	if got := transcriptTailRev(tr); got == rev {
		t.Fatal("a committed message did not refresh the tail window")
	}
	newest, ok := tr.newestLT()
	if !ok || newest != 41 {
		t.Fatalf("tail window newest LT = %d, want 41", newest)
	}
}

// TestTranscriptFollowFrameMatchesRebuild pins that the fast path renders the
// exact same frame a full rebuild does — the guard is an optimization, not a
// behaviour change.
func TestTranscriptFollowFrameMatchesRebuild(t *testing.T) {
	build := func() *transcript {
		client := aria.NewClient()
		client.SetClosedLimit(transcriptTailLimit)
		client.Apply(readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(41), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
			"type": "prose", "markdown": "streaming prose"}}}}}}}})
		tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "aria", time.Time{})
		tr.enter()
		return tr
	}
	fast := build()
	slow := build()
	for range 5 {
		fast.render()
		slow.invalidateWindow() // force the pre-optimization behaviour
		slow.render()
	}
	want := slow.lines()
	slow.invalidateWindow()
	got := fast.lines()
	if len(got) != len(want) {
		t.Fatalf("frame lines %d != rebuilt %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d differs:\n fast: %q\n full: %q", i, got[i], want[i])
		}
	}
	if fast.offset != slow.offset || fast.follow != slow.follow {
		t.Fatalf("viewport differs: %d/%v vs %d/%v", fast.offset, fast.follow, slow.offset, slow.follow)
	}
}
