package cli

import (
	"context"
	"strings"
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

const yankTestID = "3fa2c19b0d7e4f6a8b1c2d3e4f5a6b7c"

func waitClipboard(t *testing.T, tc *recordingTerminal, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if s, _ := tc.clipboard.Load().(string); s == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("clipboard = %q, want %q", tc.clipboard.Load(), want)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func yankInput(t *testing.T) (*interactiveInput, *recordingTerminal, chan struct{}) {
	t.Helper()
	reader := &stubHistoryReader{history: transcriptHistory(120)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc
	in.figaroID = yankTestID
	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	t.Cleanup(func() {
		tc.send([]byte{0x04})
		waitSignal(t, done, "input loop exit")
	})
	return in, tc, done
}

// With nothing selected, y copies the name the status bar shows and M-y the
// full id: both through the read loop, so the decoder and the arbiter are in
// the path.
func TestYankKey_NoSelectionCopiesNameAndMetaYTheID(t *testing.T) {
	for _, tc := range []struct {
		style idStyle
		want  string
	}{
		{idsAsHandles, ariaHandle(yankTestID)},
		{idsAsShortHex, yankTestID[:rpc.ShortIDLen]},
	} {
		withShownIDs(t, tc.style)
		_, term, _ := yankInput(t)
		term.send([]byte{'y'})
		waitClipboard(t, term, tc.want)
		term.send([]byte("\x1by"))
		waitClipboard(t, term, yankTestID)
	}
	if h := ariaHandle(yankTestID); !strings.Contains(h, "-") {
		t.Fatalf("the fixture id must spell a handle, got %q", h)
	}
}

// In a listing, the row's name goes to y and the row's aria id to M-y; a row
// that is not an aria leaves M-y on the attended one.
func TestYankKey_ListRowNameAndID(t *testing.T) {
	const rowID = "9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f"
	in, term, _ := yankInput(t)
	out := captured{text: "head\nrow\n", arias: map[int]listedAria{1: {id: rowID, name: "spain/some-name"}}}
	in.mu.Lock()
	in.lt.setTranscriptCmdOut(":ls", out.rows([]string{"head", "row"}))
	in.lt.tr.render()
	row, ok := in.lt.tr.pit.selected()
	in.mu.Unlock()
	if !ok || row.aria != rowID {
		t.Fatalf("the listing row is not selected: %+v %v", row, ok)
	}
	term.send([]byte{'y'})
	waitClipboard(t, term, "spain/some-name")
	term.send([]byte("\x1by"))
	waitClipboard(t, term, rowID)

	in.mu.Lock()
	in.lt.setTranscriptCmdOut(":queue", []pitRow{{text: "queued", yank: "queued text", id: "q1"}})
	in.lt.tr.render()
	in.mu.Unlock()
	term.send([]byte("\x1by"))
	waitClipboard(t, term, yankTestID)
}

var _ sync.Locker = (*sync.Mutex)(nil)
