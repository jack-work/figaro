package cli

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestHistoricalSearchExitCancelsSlowRead(t *testing.T) {
	reader := newBlockingTranscriptReader()
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	done := make(chan struct{})
	go func() {
		in.run()
		close(done)
	}()

	tc.send([]byte("/absent\r"))
	waitSignal(t, reader.started, "historical search read")
	tc.send([]byte{0x04})

	waitSignal(t, in.disconnectCh, "disconnect")
	waitSignal(t, done, "input loop exit")
	waitSignal(t, reader.canceled, "ReadBefore cancellation")
}

func TestHistoricalSearchNavigationCancelsSlowRead(t *testing.T) {
	reader := newBlockingTranscriptReader()
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	done := make(chan struct{})
	go func() {
		in.run()
		close(done)
	}()

	tc.send([]byte("/absent\r"))
	waitSignal(t, reader.started, "historical search read")
	tc.send([]byte{'j'})
	waitSignal(t, reader.canceled, "ReadBefore cancellation")

	in.mu.Lock()
	searching := in.lt.transcriptSearchingHistory()
	in.mu.Unlock()
	if searching {
		t.Fatal("navigation left canceled historical search active")
	}
	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

func TestHistoricalSearchRejectsStalePage(t *testing.T) {
	reader := newDelayedTranscriptReader()
	tc := newSearchInputTerminal()
	in := newSearchInteractiveInput(reader, tc)
	done := make(chan struct{})
	go func() {
		in.run()
		close(done)
	}()

	tc.send([]byte("/first-miss\r"))
	first := waitReadCall(t, reader.calls)
	in.mu.Lock()
	firstDone := in.searchDone
	in.mu.Unlock()
	tc.send([]byte("/second-miss\r"))
	second := waitReadCall(t, reader.calls)
	close(first.release)
	waitSignal(t, first.returned, "stale ReadBefore return")
	waitSignal(t, firstDone, "stale search worker exit")
	in.mu.Lock()
	oldest, _ := in.lt.tr.oldestLT()
	query, searching := in.lt.transcriptHistorySearch()
	in.mu.Unlock()
	if oldest != 91 {
		t.Fatalf("stale page changed oldest LT to %d, want 91", oldest)
	}
	if !searching || query != "second-miss" {
		t.Fatalf("stale page replaced current search: query=%q searching=%v", query, searching)
	}

	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
	waitSignal(t, second.returned, "current ReadBefore cancellation")
}

func TestHistoricalSearchWorkerFindsOlderResult(t *testing.T) {
	reader := &gatedHistoryReader{
		history: transcriptHistory(120),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	in := newSearchInteractiveInput(reader, newSearchInputTerminal())
	in.mu.Lock()
	in.lt.tr.findQuery("message-010")
	in.mu.Unlock()
	in.pageTranscript()
	waitSignal(t, reader.started, "historical search read")
	in.mu.Lock()
	done := in.searchDone
	in.mu.Unlock()
	close(reader.release)
	waitSignal(t, done, "historical search completion")

	in.mu.Lock()
	query, searching := in.lt.transcriptHistorySearch()
	found := false
	for _, lt := range in.lt.tr.lineLT {
		found = found || lt == 10
	}
	in.mu.Unlock()
	if searching {
		t.Fatalf("search %q did not settle", query)
	}
	if !found {
		t.Fatal("historical search did not land on LT 10")
	}
}

type blockingTranscriptReader struct {
	started  chan struct{}
	canceled chan struct{}
	start    sync.Once
	cancel   sync.Once
}

func newBlockingTranscriptReader() *blockingTranscriptReader {
	return &blockingTranscriptReader{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (r *blockingTranscriptReader) Read(context.Context, int) (aria.AriaRead, error) {
	return aria.AriaRead{}, nil
}

func (r *blockingTranscriptReader) ReadBefore(ctx context.Context, _, _ int) (aria.AriaRead, error) {
	r.start.Do(func() { close(r.started) })
	<-ctx.Done()
	r.cancel.Do(func() { close(r.canceled) })
	return aria.AriaRead{}, ctx.Err()
}

type gatedHistoryReader struct {
	history []aria.Committed
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedHistoryReader) Read(context.Context, int) (aria.AriaRead, error) {
	return aria.AriaRead{}, nil
}

func (r *gatedHistoryReader) ReadBefore(ctx context.Context, before, limit int) (aria.AriaRead, error) {
	block := false
	r.once.Do(func() {
		close(r.started)
		block = true
	})
	if block {
		select {
		case <-r.release:
		case <-ctx.Done():
			return aria.AriaRead{}, ctx.Err()
		}
	}
	return readBefore(r.history, before, limit), nil
}

type delayedReadCall struct {
	release  chan struct{}
	returned chan struct{}
}

type delayedTranscriptReader struct {
	calls chan delayedReadCall
	mu    sync.Mutex
	count int
}

func newDelayedTranscriptReader() *delayedTranscriptReader {
	return &delayedTranscriptReader{calls: make(chan delayedReadCall, 2)}
}

func (r *delayedTranscriptReader) Read(context.Context, int) (aria.AriaRead, error) {
	return aria.AriaRead{}, nil
}

func (r *delayedTranscriptReader) ReadBefore(ctx context.Context, before, limit int) (aria.AriaRead, error) {
	call := delayedReadCall{release: make(chan struct{}), returned: make(chan struct{})}
	r.mu.Lock()
	r.count++
	first := r.count == 1
	r.mu.Unlock()
	r.calls <- call
	if first {
		<-call.release
	} else {
		select {
		case <-call.release:
		case <-ctx.Done():
			close(call.returned)
			return aria.AriaRead{}, ctx.Err()
		}
	}
	close(call.returned)
	return readBefore(transcriptHistory(120), before, limit), nil
}

type searchInputTerminal struct {
	reads chan []byte
}

func newSearchInputTerminal() *searchInputTerminal {
	return &searchInputTerminal{reads: make(chan []byte, 8)}
}

func (t *searchInputTerminal) send(p []byte) {
	t.reads <- append([]byte(nil), p...)
}

func (t *searchInputTerminal) MakeRaw() (func(), error) { return func() {}, nil }
func (t *searchInputTerminal) Size() (int, int)         { return 80, 12 }
func (t *searchInputTerminal) OnResize(func(int, int)) func() {
	return func() {}
}
func (t *searchInputTerminal) Read(p []byte) (int, error) {
	data, ok := <-t.reads
	if !ok {
		return 0, io.EOF
	}
	return copy(p, data), nil
}
func (t *searchInputTerminal) SetClipboard(string) {}
func (t *searchInputTerminal) IsTTY() bool         { return true }

func newSearchInteractiveInput(reader transcriptReadClient, tc *searchInputTerminal) *interactiveInput {
	out := ldrender.NewFakeTerminal(80, 12)
	settings := &renderSettings{}
	lt := newLivelogTurn(out, 80, 12, settings, "", time.Time{}, nil, nil, nil)
	lt.enterTranscript()
	lt.apply(readBefore(transcriptHistory(120), recentCursor, transcriptPageSize))
	listen := false
	return &interactiveInput{
		tc: tc, lt: lt, fcli: reader, mu: &sync.Mutex{}, set: settings,
		listen: &listen, cancel: func() {}, disconnectCh: make(chan struct{}, 1),
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitReadCall(t *testing.T, ch <-chan delayedReadCall) delayedReadCall {
	t.Helper()
	select {
	case call := <-ch:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ReadBefore")
		return delayedReadCall{}
	}
}
