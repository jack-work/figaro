package cli

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

type recordingTerminal struct {
	*searchInputTerminal
	clipboard atomic.Value
}

func newRecordingTerminal() *recordingTerminal {
	rt := &recordingTerminal{searchInputTerminal: newSearchInputTerminal()}
	rt.clipboard.Store("")
	return rt
}

func (t *recordingTerminal) SetClipboard(s string) { t.clipboard.Store(s) }

type stubHistoryReader struct{ history []aria.TurnPart }

func (r *stubHistoryReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *stubHistoryReader) ReadBefore(_ context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	before := int(at.Turn)
	return readBefore(r.history, before, limit), nil
}

func (r *stubHistoryReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// With a selection, 'y' copies the selection, not the aria id.
func TestYankKey_WithSelectionCopiesSelection(t *testing.T) {
	reader := &stubHistoryReader{history: transcriptHistory(120)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc
	in.figaroID = "aria-99"

	in.mu.Lock()
	in.lt.tr.selectNode(-1, false)
	plan, active := in.lt.transcriptSelectionPlan()
	in.mu.Unlock()
	if !active {
		t.Fatal("failed to seed selection")
	}

	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	tc.send([]byte{'y'})

	deadline := time.After(2 * time.Second)
	for {
		if s, _ := tc.clipboard.Load().(string); s != "" && s != "aria-99" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("clipboard did not receive selection text (got %q); plan=%+v",
				tc.clipboard.Load(), plan)
		case <-time.After(20 * time.Millisecond):
		}
	}

	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

func TestYankKey_NoSelectionCopiesAriaID(t *testing.T) {
	reader := &stubHistoryReader{history: transcriptHistory(120)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc
	in.figaroID = "aria-42"

	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	tc.send([]byte{'y'})

	deadline := time.After(time.Second)
	for {
		if s, _ := tc.clipboard.Load().(string); s == "aria-42" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("clipboard did not receive aria id (got %q)", tc.clipboard.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}

	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

var _ sync.Locker = (*sync.Mutex)(nil)
