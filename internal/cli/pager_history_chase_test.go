package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Gluck, 2026-11-08: typing ':version' during a long write tool call made the
// pager jump. The first read held only the clipped tail of the last turn (a
// tool call's arguments filled the byte budget), so the screen was nearly
// empty, and nothing asked for older history until a key arrived; the ':' then
// paged it in, one relayout per page, under his typing. History is chased as
// soon as the pager wants it.

type clippedTailReader struct {
	history []aria.TurnPart
	mu      sync.Mutex
	reads   []aria.Anchor
	read    chan struct{}
}

func (r *clippedTailReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *clippedTailReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func (r *clippedTailReader) ReadBefore(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	r.mu.Lock()
	r.reads = append(r.reads, at)
	r.mu.Unlock()
	select {
	case r.read <- struct{}{}:
	default:
	}
	if at.Turn == recentCursor {
		page := readBefore(r.history, recentCursor, 1)
		page.More.Before = true
		return page, nil
	}
	return readBefore(r.history, int(at.Turn), transcriptPageSize), nil
}

func (r *clippedTailReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reads)
}

func clippedTailInput(reader *clippedTailReader) *interactiveInput {
	settings := &renderSettings{}
	lt := newLivelogTurn(ldrender.NewFakeTerminal(80, 12), 80, 12, settings, "", time.Time{}, nil, nil, nil)
	return &interactiveInput{
		tc: newSearchInputTerminal(), lt: lt, fcli: reader, mu: new(renderLock), set: settings,
		cancel: func() {}, disconnectCh: make(chan struct{}, 1),
	}
}

func TestACatchUpThatLeavesTheScreenShortChasesHistoryWithoutAKey(t *testing.T) {
	reader := &clippedTailReader{history: transcriptHistory(60), read: make(chan struct{}, 64)}
	in := clippedTailInput(reader)
	in.lt.enterTranscript()
	in.readHistoryIntoPager()
	awaitChase(t, in, reader)
}

// The pacer may refuse the frame the read asks for, so nothing has settled
// the window when the chase is decided.
func TestEnteringThePagerOnAClippedTailChasesHistoryWithoutAKey(t *testing.T) {
	reader := &clippedTailReader{history: transcriptHistory(60), read: make(chan struct{}, 64)}
	in := clippedTailInput(reader)
	refused := false
	in.lt.tr.gate = func() bool {
		if !refused {
			refused = true
			return false
		}
		return true
	}
	in.enterTranscript()
	awaitChase(t, in, reader)
}

func awaitChase(t *testing.T, in *interactiveInput, reader *clippedTailReader) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for reader.count() < 2 {
		select {
		case <-reader.read:
		case <-deadline:
			t.Fatalf("the catch-up held %d message(s) on a 12-row pane and asked for nothing older",
				in.lt.client.Count())
		}
	}
	for {
		in.mu.Lock()
		done := in.pageDone
		in.mu.Unlock()
		if done == nil {
			break
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the history chase never finished")
		}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.lt.transcriptWantsHistory() {
		t.Fatalf("the chase stopped while the pager still wants history (%d messages held)", in.lt.client.Count())
	}
}

func TestAShortTranscriptWantsHistoryOnlyWhileMoreIsHeld(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	lt.enterTranscript()
	tail := readBefore(transcriptHistory(60), recentCursor, 1)
	lt.apply(tail)
	lt.setMoreBefore(true)
	lt.render()
	if !lt.transcriptWantsHistory() {
		t.Fatal("one message on a 12-row pane with more before it: the pager must want history")
	}
	lt.setMoreBefore(false)
	lt.render()
	if lt.transcriptWantsHistory() {
		t.Fatal("at the floor of the aria there is nothing to want")
	}
}
