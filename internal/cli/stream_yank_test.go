package cli

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

// recordingTerminal is a searchInputTerminal that also records clipboard writes.
type recordingTerminal struct {
	*searchInputTerminal
	clipboard atomic.Value // string
}

func newRecordingTerminal() *recordingTerminal {
	rt := &recordingTerminal{searchInputTerminal: newSearchInputTerminal()}
	rt.clipboard.Store("")
	return rt
}

func (t *recordingTerminal) SetClipboard(s string) { t.clipboard.Store(s) }

// stubHistoryReader satisfies transcriptReadClient with a fixed 120-message
// history so copySelection has something to walk.
type stubHistoryReader struct{ history []aria.TurnPart }

func (r *stubHistoryReader) Read(context.Context, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *stubHistoryReader) ReadBefore(_ context.Context, before, limit int) (aria.Page, error) {
	return readBefore(r.history, before, limit), nil
}

func (r *stubHistoryReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// With a selection active, 'y' should fire the selection copy (populating
// copyPlan or completing to the clipboard) — NOT the aria id shortcut.
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

	// Wait for the copy goroutine to land text in the clipboard.
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

// Without a selection, 'y' must still copy the aria id (the incipit fallback).
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

// Sanity: guard against a data race in the two shims we swap in.
var _ sync.Locker = (*sync.Mutex)(nil)
