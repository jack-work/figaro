package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Detached by one notch, the pager keeps drawing the live turn and moves no
// row above the viewport.
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
	client.Apply(live(0, 1, 0, "TICKONE"), aria.Notify)

	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k')
	if tr.follow {
		t.Fatal("k did not detach")
	}
	before := append([]string(nil), tr.lines()...)
	if !strings.Contains(strings.Join(before, "\n"), "TICKONE") {
		t.Fatalf("the detached view does not show the live turn at all:\n%s", strings.Join(before, "\n"))
	}
	offset := tr.offset

	// Releasing the head (Live.From past node 0) must not make content vanish.
	client.Apply(live(0, 2, 1, "TICKTWO"), aria.Notify)
	client.Apply(live(1, 3, 1, "TICKTWO"), aria.Notify)
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
	// Growth appends at the end of line space.
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
