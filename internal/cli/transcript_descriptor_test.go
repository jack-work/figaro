package cli

import (
	"fmt"
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

func TestTranscript_CommittedWatermarkReconcilesHeldOpen(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(10), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "partial"},
	}}}}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k')
	if tr.heldOpen == nil {
		t.Fatal("open message was not held")
	}
	committed := aria.Message{
		Turn: 10, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "complete"}},
	}
	tr.observeCommitted(committed)
	if tr.heldOpen == nil || tr.heldOpen.Nodes[0].Markdown != "complete" || tr.committedW != 10 {
		t.Fatalf("committed open lane = %+v, watermark=%d", tr.heldOpen, tr.committedW)
	}
}
