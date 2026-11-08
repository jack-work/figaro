package cli

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Under --lockstep the spike tape (a 500-row dump ten turns back) hung the
// history chase: a following pager cuts its window under the dump to fit
// the row budget, so it keeps wanting older rows while the store already
// holds them. Every page asked for at the cut brought nothing the window
// would keep, and the next request was the same one, forever.

type tallDumpReader struct {
	history []aria.TurnPart
	reads   atomic.Int64
}

func (r *tallDumpReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *tallDumpReader) ReadBefore(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	r.reads.Add(1)
	return readBefore(r.history, int(at.Turn), transcriptPageSize), nil
}

func (r *tallDumpReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func tallDumpHistory() []aria.TurnPart {
	h := transcriptHistory(120)
	var dump strings.Builder
	for i := range 3 * transcriptWindowRows {
		fmt.Fprintf(&dump, "dump line %d\n\n", i)
	}
	h[109].Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Markdown: dump.String()}}
	return h
}

func tallDumpPager(t *testing.T, replaying bool) (*interactiveInput, *tallDumpReader) {
	t.Helper()
	reader := &tallDumpReader{history: tallDumpHistory()}
	settings := &renderSettings{}
	lt := newLivelogTurn(ldrender.NewFakeTerminal(100, 33), 100, 33, settings, "", time.Time{}, nil, nil, nil)
	in := &interactiveInput{
		tc: newSearchInputTerminal(), lt: lt, fcli: reader, mu: new(renderLock), set: settings,
		cancel: func() {}, disconnectCh: make(chan struct{}, 1), replaying: replaying,
	}
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: reader.history})
	in.lt.tr.buildIndex()
	in.mu.Lock()
	_, below := in.lt.client.Before(in.lt.tr.from, 1)
	from := in.lt.tr.from
	in.mu.Unlock()
	if below == 0 {
		t.Fatalf("fixture: the window starts at turn %d with nothing held below it; want a cut", from.Turn)
	}
	return in, reader
}

func chaseSettles(t *testing.T, in *interactiveInput, reader *tallDumpReader) {
	t.Helper()
	in.chaseHistory()
	in.mu.Lock()
	done := in.pageDone
	in.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("the history chase is still paging after %d reads", reader.reads.Load())
		}
	}
	if n := reader.reads.Load(); n > 0 {
		t.Fatalf("the chase read %d pages below a window cut over history the store already holds", n)
	}
}

func TestAFollowingPagerCutUnderATallTurnDoesNotPageForever(t *testing.T) {
	in, reader := tallDumpPager(t, false)
	chaseSettles(t, in, reader)
}

func TestAReplayChasesHistoryAndSettles(t *testing.T) {
	in, reader := tallDumpPager(t, true)
	chaseSettles(t, in, reader)
}
