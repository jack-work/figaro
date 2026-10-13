package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// A live frame must move neither windowRev nor the interval's floor.
func transcriptTailRev(tr *transcript) uint64 { return tr.windowRev }

func TestTranscriptFollowFrameDoesNotRebuildWindow(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
	ft := ldrender.NewFakeTerminal(50, 10)
	tr := newTranscript(ft, 50, 10, ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()

	rev := transcriptTailRev(tr)
	if rev == 0 {
		t.Fatal("entering the pager should establish the tail window")
	}
	floor := tr.from
	if floor == (aria.Anchor{}) {
		t.Fatal("the window has no floor")
	}
	for range 20 {
		tr.render()
	}
	if got := transcriptTailRev(tr); got != rev {
		t.Fatalf("live frames rebuilt the window: rev %d -> %d", rev, got)
	}
	if tr.from != floor {
		t.Fatalf("live frames moved the window floor: %v -> %v", floor, tr.from)
	}

	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: uint64(41), Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "message-041"}},
	}}}}, aria.Notify)
	tr.render()
	if got := transcriptTailRev(tr); got == rev {
		t.Fatal("a committed message did not refresh the tail window")
	}
	held := tr.messages()
	if newest := held[len(held)-1].Turn; newest != 41 {
		t.Fatalf("tail window newest LT = %d, want 41", newest)
	}
}

func TestTranscriptFollowFrameMatchesRebuild(t *testing.T) {
	build := func() *transcript {
		client := aria.NewClient()
		client.SetClosedLimit(transcriptTailLimit)
		applyTail(client, readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(41), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
			"type": "prose", "markdown": "streaming prose"}}}}}}}}, aria.Notify)
		tr := newTranscript(ldrender.NewFakeTerminal(50, 10), 50, 10, ldrender.NodeText{}, client, "aria", time.Time{})
		tr.enter()
		return tr
	}
	fast := build()
	slow := build()
	for range 5 {
		fast.render()
		slow.invalidateWindow()
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
