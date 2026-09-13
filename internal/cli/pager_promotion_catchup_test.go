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
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// THE PAGER OPENS BY ITSELF, AND USED TO OPEN ON NOTHING.
//
// Reported from Windows: "when incipit mode is automatically activated the
// transcript makes it appear as though the message that spawned incipit mode
// and started the cli is the first message, even if there was prior history."
//
// Ctrl-T and `figaro listen` read the aria's tail before they open the pager.
// The pager also opens WITHOUT being asked, an open turn taller than the
// viewport (OnLive -> openOverflows) or a resize that shrinks the viewport
// under the live region, and those two doors read nothing at all. The store
// then held one turn, MoreBefore was never set by any wire answer, and
// atAriaFloor declared the running question the beginning of the aria: no
// history above it, and no page ever requested, so scrolling up found nothing.
//
// Reproduced in a real terminal at 100x24 before the fix: an aria with five
// prior turns, promoted by an overflowing reply, sat at `1-22/37`: 37 rows
// being that one turn, and six half-page-ups moved nothing. The same aria,
// same binary, entered with Ctrl-T instead: `133-153/153`.
// ---------------------------------------------------------------------------

// promotionFixture is a livelogTurn whose viewport is too short for the turn it
// is about to be handed: i.e. one that will promote itself.
func promotionFixture(tb testing.TB, w, h int) *livelogTurn {
	tb.Helper()
	var out bytes.Buffer
	return newLivelogTurn(&out, w, h, &renderSettings{}, "aria1234", time.Unix(0, 0),
		newSessionStatus("aria1234", time.Unix(0, 0)), nil, dimRule)
}

// tallTurn is a page whose open turn is taller than any viewport we test with.
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

// TestOverflowPromotionAsksForHistory: the auto-promotion door owes a read, and
// pays it. Canary: delete the catchUp call in enterPager and this fails with
// "the pager opened on its own and asked for no history".
func TestOverflowPromotionAsksForHistory(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	asked := 0
	lt.setCatchUp(func() { asked++ })

	lt.apply(tallTurn(7, 40)) // taller than 12 rows: promotes on the frame path

	if !lt.transcriptActive() {
		t.Fatal("fixture: the turn did not overflow into the pager")
	}
	if asked != 1 {
		t.Fatalf("the pager opened on its own and asked for history %d times, want 1", asked)
	}
}

// TestResizePromotionAsksForHistory: the OTHER automatic door: the viewport
// shrinking under a live region: owes the same read.
func TestResizePromotionAsksForHistory(t *testing.T) {
	lt := promotionFixture(t, 60, 40)
	asked := 0
	lt.setCatchUp(func() { asked++ })

	lt.apply(tallTurn(7, 12)) // fits 40 rows: stays inline
	if lt.transcriptActive() {
		t.Fatal("fixture: the turn promoted before the resize, so the resize proves nothing")
	}
	lt.resize(60, 12) // now it cannot be painted inline

	if !lt.transcriptActive() {
		t.Fatal("fixture: the destructive resize did not promote")
	}
	if asked != 1 {
		t.Fatalf("the resize promoted and asked for history %d times, want 1", asked)
	}
}

// TestSeededPromotionDoesNotReRead: a session that already fetched a catch-up
// page (it joined a turn it did not open) opens the pager on that page. Asking
// the wire again would be a second round trip for content in hand.
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

// TestPromotedPagerBelievesNothingAboutTheBeginning is the assertion closest to
// what the user SAW. Without a catch-up, a promoted pager holds one turn and
// MoreBefore false, and atAriaFloor then reports that the live question is the
// first message in the aria. The hook is what makes that answer arrive from the
// wire instead of from a zero value.
func TestPromotedPagerBelievesNothingAboutTheBeginning(t *testing.T) {
	lt := promotionFixture(t, 60, 12)
	// No catch-up armed: this is the OLD behaviour, pinned so the claim in the
	// commit message is checkable rather than asserted.
	lt.apply(tallTurn(7, 40))
	if !lt.tr.atAriaFloor() {
		t.Fatal("fixture: without a catch-up the pager should stand on a false floor")
	}
	if _, want := lt.tr.pageCursor(); want {
		t.Fatal("fixture: a pager that believes it is at the floor asks for no page")
	}

	// With the read paid: the same fold the deliberate door uses: the floor is
	// the wire's answer, and the window can walk backwards again.
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

// failingOnceReader fails the first catch-up read and serves history after.
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

// A TIMEOUT IS NOT A FLOOR. The catch-up read that a subject switch owes can
// fail, and when it did the pager kept an empty window with nothing to ask
// again: the reader saw a conversation with no content in it and could only
// leave the session. The clock asks again.
func TestFailedCatchUpIsRetried(t *testing.T) {
	reader := &failingOnceReader{failFor: 1, history: transcriptHistory(40)}
	out := ldrender.NewFakeTerminal(80, 12)
	settings := &renderSettings{}
	lt := newLivelogTurn(out, 80, 12, settings, "", time.Time{}, nil, nil, nil)
	lt.enterTranscript()
	in := &interactiveInput{
		tc: newSearchInputTerminal(), lt: lt, fcli: reader, mu: &sync.Mutex{}, set: settings,
		cancel: func() {}, disconnectCh: make(chan struct{}, 1),
	}

	in.readHistoryIntoPager() // the seed a switch performs: it fails
	if in.caughtUp {
		t.Fatal("a failed read claimed the window")
	}
	if len(lt.tr.messages()) != 0 {
		t.Fatal("fixture: the failed read left content behind")
	}

	in.reseedIfEmpty() // what the pager's clock does
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
