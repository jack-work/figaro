package cli

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The pager's automatic doors (an overflowing open turn, a shrinking resize)
// owe the same catch-up read Ctrl-T pays, or atAriaFloor calls the running
// question the beginning of the aria.
func promotionFixture(tb testing.TB, w, h int) *livelogTurn {
	tb.Helper()
	var out bytes.Buffer
	return newLivelogTurn(&out, w, h, &renderSettings{}, "aria1234", time.Unix(0, 0),
		newSessionStatus("aria1234", time.Unix(0, 0)), nil, dimRule)
}

func tallTurn(turn uint64, rows int) aria.Page {
	nodes := make([]livedoc.Node, 0, rows)
	for i := range rows {
		nodes = append(nodes, livedoc.Node{
			Type: livedoc.NodeProse, Markdown: fmt.Sprintf("ROW%d-%d", turn, i),
		})
	}
	return aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: turn, Inquiry: "THE QUESTION THAT STARTED THE CLI", Nodes: nodes,
	}}}}
}

func TestOverflowPromotionAsksForHistory(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	asked := 0
	lt.setCatchUp(func() { asked++ })

	lt.apply(tallTurn(7, 40))

	if !lt.transcriptActive() {
		t.Fatal("fixture: the turn did not overflow into the pager")
	}
	if asked != 1 {
		t.Fatalf("the pager opened on its own and asked for history %d times, want 1", asked)
	}
}

func TestResizePromotionAsksForHistory(t *testing.T) {
	lt := promotionFixture(t, 60, 40)
	asked := 0
	lt.setCatchUp(func() { asked++ })

	lt.apply(tallTurn(7, 12))
	if lt.transcriptActive() {
		t.Fatal("fixture: the turn promoted before the resize, so the resize proves nothing")
	}
	lt.resize(60, 12)

	if !lt.transcriptActive() {
		t.Fatal("fixture: the destructive resize did not promote")
	}
	if asked != 1 {
		t.Fatalf("the resize promoted and asked for history %d times, want 1", asked)
	}
}

func TestSeededPromotionDoesNotReRead(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	asked := 0
	lt.setCatchUp(func() { asked++ })
	seed := sealedPage(aria.Message{Turn: 5, Inquiry: "EARLIER", Nodes: []livedoc.Node{
		{Type: livedoc.NodeProse, Markdown: "EARLIER REPLY"},
	}})
	seed.More.Before = true
	lt.openInline(seed)

	lt.apply(tallTurn(7, 40))

	if !lt.transcriptActive() {
		t.Fatal("fixture: the turn did not overflow into the pager")
	}
	if asked != 0 {
		t.Fatalf("the pager had a seed and still asked the wire %d times", asked)
	}
	if !lt.client.MoreBefore() {
		t.Fatal("the seed's own answer about the beginning was dropped")
	}
}

// atAriaFloor must answer from the wire, not from a zero MoreBefore.
func TestPromotedPagerBelievesNothingAboutTheBeginning(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	lt.apply(tallTurn(7, 40))
	if !lt.tr.atAriaFloor() {
		t.Fatal("fixture: without a catch-up the pager should stand on a false floor")
	}
	if _, want := lt.tr.pageCursor(); want {
		t.Fatal("fixture: a pager that believes it is at the floor asks for no page")
	}

	lt2 := promotionFixture(t, 60, 12)
	lt2.setCatchUp(func() {
		lt2.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: 6, Inquiry: "AN EARLIER QUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "AN EARLIER REPLY"}},
		}}}})
		lt2.setMoreBefore(true)
	})
	lt2.apply(tallTurn(7, 40))
	if lt2.tr.atAriaFloor() {
		t.Fatal("the catch-up landed and the pager still calls the live turn the beginning")
	}
	found := false
	for _, m := range lt2.tr.messages() {
		if m.Turn == 6 {
			found = true
		}
	}
	if !found {
		t.Fatal("the history the catch-up read is not in the promoted pager's window")
	}
}

type failingOnceReader struct {
	mu      sync.Mutex
	calls   int
	failFor int
	history []aria.TurnPart
}

func (r *failingOnceReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (r *failingOnceReader) ReadBefore(_ context.Context, at aria.Anchor, limit int) (aria.Page, error) {
	r.mu.Lock()
	r.calls++
	fail := r.calls <= r.failFor
	r.mu.Unlock()
	if fail {
		return aria.Page{}, context.DeadlineExceeded
	}
	return readBefore(r.history, int(at.Turn), limit), nil
}

func (r *failingOnceReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// A failed catch-up read is not a floor: the clock asks again.
func TestFailedCatchUpIsRetried(t *testing.T) {
	reader := &failingOnceReader{failFor: 1, history: transcriptHistory(40)}
	out := ldrender.NewFakeTerminal(80, 12)
	settings := &renderSettings{}
	lt := newLivelogTurn(out, 80, 12, settings, "", time.Time{}, nil, nil, nil)
	lt.enterTranscript()
	in := &interactiveInput{
		tc: newSearchInputTerminal(), lt: lt, fcli: reader, mu: new(renderLock), set: settings,
		cancel: func() {}, disconnectCh: make(chan struct{}, 1),
	}

	in.readHistoryIntoPager()
	if in.caughtUp {
		t.Fatal("a failed read claimed the window")
	}
	if len(lt.tr.messages()) != 0 {
		t.Fatal("fixture: the failed read left content behind")
	}

	in.reseedIfEmpty()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		in.mu.Lock()
		got := len(lt.tr.messages())
		in.mu.Unlock()
		if got > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the window is still empty after the retry (reads=%d)", reader.calls)
}
