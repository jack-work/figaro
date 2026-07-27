package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE PAGE DESCRIPTORS ARE GONE. This file used to pin pageDesc (the hash that
// let an evicted page be replayed and verified), readNextPage's sparse forward
// probe, and the bounded descriptor chain. All three existed to serve a SECOND
// COPY of history the pager kept beside the client; with one owner there is no
// page to evict, replay or verify, and no forward direction to page in.
//
// What survives is the behaviour that machinery was hiding: bug B.

// TestDetachedTailAdvancesAndScreenHoldsStill is BUG B's canary, at unit
// scale: the pager, detached by one notch, must keep drawing the live turn as
// it arrives AND must not move a single row above the viewport while it does.
//
// It replaces TestTranscript_CommittedWatermarkReconcilesHeldOpen, which
// pinned the opposite: that the frozen snapshot (heldOpen) was kept in sync
// with what committed. There is no snapshot now — the window is the store's
// tail interval and the open turn is the last thing in it — so both halves are
// asserted directly. Restore heldOpen and the first half fails; make the
// window re-derive its floor while detached and the second half fails.
func TestDetachedTailAdvancesAndScreenHoldsStill(t *testing.T) {
	client := aria.NewClient()
	applyTail(client, readBefore(transcriptHistory(12), recentCursor, transcriptPageSize))
	live := func(from uint64, v int, id uint64, text string) aria.Page {
		return aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 13, Live: &aria.Live{
			From: from, V: v, Nodes: []aria.NodeDelta{{ID: id, Set: map[string]any{
				"type": string(livedoc.NodeProse), "role": livedoc.RoleOutput, "markdown": text,
			}}},
		}}}}}
	}
	client.Apply(live(0, 1, 0, "TICKONE"))

	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k') // detach by ONE notch: the tail is still on screen
	if tr.follow {
		t.Fatal("k did not detach")
	}
	before := append([]string(nil), tr.lines()...)
	if !strings.Contains(strings.Join(before, "\n"), "TICKONE") {
		t.Fatalf("the detached view does not show the live turn at all:\n%s", strings.Join(before, "\n"))
	}
	offset := tr.offset

	// The turn goes on: one more streamed node, and then the head is RELEASED
	// (Live.From advances past node 0), which is the move that used to make
	// content vanish if the open message were drawn live.
	client.Apply(live(0, 2, 1, "TICKTWO"))
	client.Apply(live(1, 3, 1, "TICKTWO"))
	tr.render()
	after := tr.lines()

	joined := strings.Join(after, "\n")
	if !strings.Contains(joined, "TICKTWO") {
		t.Fatalf("the detached tail did not advance:\n%s", joined)
	}
	if strings.Count(joined, "TICKONE") != 1 {
		t.Fatalf("the released node vanished or doubled (%d copies):\n%s",
			strings.Count(joined, "TICKONE"), joined)
	}
	// THE OTHER HALF: nothing above the viewport moved. Growth appends at the
	// end of line space, so every line the reader can see is where it was.
	if tr.offset != offset {
		t.Fatalf("the viewport moved: offset %d -> %d", offset, tr.offset)
	}
	for i := 0; i < offset && i < len(before) && i < len(after); i++ {
		if before[i] != after[i] {
			t.Fatalf("line %d above the viewport changed:\n before: %q\n after:  %q",
				i, before[i], after[i])
		}
	}
}
