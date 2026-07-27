package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestPageDescExactReplay(t *testing.T) {
	history := transcriptHistory(90)
	messages := committedMessages(aria.Page{Parts: history[30:60]})
	desc := describePage(messages)
	if desc.FirstTurn != 31 || desc.LastTurn != 60 || desc.Count != 30 || desc.ReplayBefore != 61 {
		t.Fatalf("descriptor = %+v", desc)
	}
	replayed := committedMessages(readBefore(history, desc.ReplayBefore, desc.Count))
	if got := describePage(replayed); !desc.equal(got) {
		t.Fatalf("replay descriptor = %+v, want %+v", got, desc)
	}
	replayed[0].Turn++
	if desc.equal(describePage(replayed)) {
		t.Fatal("LT hash accepted a changed page")
	}
}

func TestReadNextPageFindsImmediateSparseSuccessors(t *testing.T) {
	var history []aria.TurnPart
	for i := 1; i <= 100; i++ {
		lt := i * 10
		history = append(history, aria.TurnPart{Turn: aria.Turn{ID: uint64(lt), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("message-%d", lt)}}}})
	}
	probes := 0
	r, err := readNextPage(100, 1000, 3, func(before, limit int) (aria.Page, error) {
		probes++
		return readBefore(history, before, limit), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := committedMessages(r)
	if len(got) != 3 || got[0].Turn != 110 || got[2].Turn != 130 {
		t.Fatalf("next sparse page = %+v", describePage(got))
	}
	if probes > 64 {
		t.Fatalf("fallback used %d probes", probes)
	}
}

func TestTranscript_BoundedDescriptorsFallBackForward(t *testing.T) {
	history := transcriptHistory((transcriptDescLimit + 8) * transcriptPageSize)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range transcriptDescLimit + 5 {
		tr.offset = 0
		tr.checkOlder = true
		req, ok := tr.pageCursor()
		if !ok {
			t.Fatal("expected older page")
		}

		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	if len(tr.pages) != transcriptPageLimit || len(tr.newer) != transcriptDescLimit ||
		len(tr.payloadLRU) != transcriptPayloadLRULimit {
		t.Fatalf("cache sizes: window=%d payload=%d desc=%d", len(tr.pages), len(tr.payloadLRU), len(tr.newer))
	}
	firstReplay := true
	for len(tr.newer) > 0 {
		tr.offset = len(tr.lineKey)
		tr.checkNewer = true
		req, ok := tr.pageCursor()
		if !ok || req.expected.Count == 0 {
			t.Fatalf("descriptor replay request = %+v, %v", req, ok)
		}
		if firstReplay && len(req.cached) == 0 {
			t.Fatal("nearest evicted payload missed the LRU")
		}
		firstReplay = false
		messages := committedMessages(readBefore(history, req.before, req.expected.Count))
		if len(req.cached) > 0 {
			messages = req.cached
		}
		tr.applyPage(req, messages)
	}
	tr.offset = len(tr.lineKey)
	tr.checkNewer = true
	req, ok := tr.pageCursor()
	if !ok || req.after == 0 {
		t.Fatalf("fallback request = %+v, %v", req, ok)
	}
	r, err := readNextPage(req.after, req.watermark, transcriptPageSize, func(before, limit int) (aria.Page, error) {
		return readBefore(history, before, limit), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := tr.messages()[len(tr.messages())-1].Turn
	tr.applyPage(req, committedMessages(r))
	after := tr.messages()[len(tr.messages())-1].Turn
	if after <= before || after-before != transcriptPageSize {
		t.Fatalf("fallback advanced %d -> %d", before, after)
	}
}

func TestTranscript_DescriptorMismatchInvalidatesReplayChain(t *testing.T) {
	history := transcriptHistory(150)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 4 {
		tr.offset = 0
		tr.checkOlder = true
		req, _ := tr.pageCursor()
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	tr.offset = len(tr.lineKey)
	tr.checkNewer = true
	req, ok := tr.pageCursor()
	if !ok || len(tr.newer) < 2 {
		t.Fatal("expected replay chain")
	}
	changed := readBefore(history, req.before, req.expected.Count)
	changed.Parts[0].ID++
	tr.applyPage(req, committedMessages(changed))
	if len(tr.newer) != 0 || !tr.checkNewer {
		t.Fatalf("mismatch left %d descriptors, checkNewer=%v", len(tr.newer), tr.checkNewer)
	}
}

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
	client.Apply(readBefore(transcriptHistory(12), recentCursor, transcriptPageSize))
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
