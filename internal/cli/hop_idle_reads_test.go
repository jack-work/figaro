package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A PAGER THAT IS DOING NOTHING MUST ASK FOR NOTHING. The bench drove four
// hops over 68 seconds and caught 494 history reads at anchor 0, one every
// 24 ms for 45 seconds, 439 of them while the runtime said IDLE. They began on
// the first hop BACK onto an aria already seen, which is the path that takes a
// subject off the shelf.
//
// The shape is a hole the pager can see and the wire cannot fill: it asks, the
// answer changes nothing, the hole is still there on the next frame, and the
// per-hop saving is spent back many times over between hops.
//
// This canary is the bench's finding made cheap: no daemon, no pane, one
// counter, and the question is only ever "how many times did it ask".
func TestHop_AnIdlePagerAsksForNothing(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	// The parent, whole.
	in.lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT")})
	in.lt.setMoreBefore(false)
	in.lt.tr.buildIndex()

	// A HOP TO A RELATIVE, the shape the reviewer reduced the loop to: the
	// clone keeps turns 1 and 2 and says there is more above them, and the
	// child's own tail page says there is not.
	in.parkSubject("ariaAAAA")
	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 3)
	in.lt.apply(aria.Page{Parts: partsFor(3, 2, "BRANCH"), More: aria.More{Before: true}})
	in.lt.tr.buildIndex()

	// Now sit still. Every frame the pager draws, it may ask the wire for one
	// thing; an idle pager over a conversation it holds whole must ask for
	// none of them.
	asked := 0
	for range 40 {
		in.lt.tr.buildIndex()
		req, want := in.lt.tr.pageCursor()
		if !want {
			continue
		}
		asked++
		if req.fill != nil {
			t.Fatalf("frame %d: the pager wants the hole %v..%v filled, on a conversation it holds whole",
				asked, req.fill.From, req.fill.To)
		}
		t.Fatalf("frame %d: the pager wants a read at turn %d on a conversation it holds whole",
			asked, req.at.Turn)
	}
}

// A HOP BACK ONTO A PARKED ARIA THAT IS NOT SURE OF ITS TAIL OWES ONE READ.
//
// The reviewer's matrix (27068b2c) found the sentinel surviving a Ctrl-O back
// onto an aria that had looked clean when reached with `a`. The shelf is why:
// a switch that decides it owes the wire nothing never folds a page, and
// folding a page is the only thing that corrects a store's belief about what
// lies above it. So a parked store that says "there is more above" must be
// read once, whether that belief is right (the turns that arrived while we
// were away) or wrong (a sentinel over a conversation we hold whole).
func TestHop_AdoptingAnUnsureTailOwesOneRead(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	// A tail window that knows there is more above it: the aria kept running.
	in.lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT"), More: aria.More{After: true}})
	in.lt.tr.follow = false
	in.lt.tr.from = aria.Anchor{Turn: 1}
	in.lt.tr.buildIndex()

	in.parkSubject("ariaAAAA")
	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 0)
	in.lt.apply(aria.Page{Parts: partsFor(1, 2, "OTHER")})

	in.parkSubject("ariaBBBB")
	p := in.takeParked("ariaAAAA")
	if p == nil {
		t.Fatal("the parent is not on the shelf")
	}
	if !p.client.MoreAfter() {
		t.Fatal("fixture: the parked store is sure of its tail, so there is nothing to test")
	}
	plan := in.lt.adopt("ariaAAAA", newSessionStatus("ariaAAAA", time.Now()), p, 0)
	if plan.kind == seedNothing {
		t.Fatal("the switch owes the wire nothing while its store says the tail is not the tail")
	}
}

// A HEAD FORK TAKEN WHILE THE PARENT IS MID-TURN LEAVES NO HOLE.
//
// Found in a real pane by the reviewer (27068b2c): parent with five sealed
// turns and a live turn six (prose, then a tool blocked on a FIFO), fork at
// the head, the child begins turn seven. The lineage says the two share
// everything below seven, which is an upper bound and not a fact: the clone
// drops the OPEN turn on purpose, so what it kept was five turns, and a read
// floored at seven stepped straight over turn six. The pane showed "1 turn not
// loaded" between five and seven and no amount of scrolling cleared it,
// because nothing ever asked for it; only a jump did.
func TestHop_AForkOffALiveTurnLeavesNoHole(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT")})
	// Turn six is OPEN: it has content and no seal.
	in.lt.apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{
			ID: 6, Inquiry: "the live one",
			Nodes: partsFor(6, 1, "LIVE")[0].Nodes,
			Live:  &aria.Live{From: 0, V: 1},
		},
	}}})
	if in.lt.client.Open() == nil {
		t.Fatal("fixture: turn 6 is not open, so there is nothing to drop")
	}

	// The head fork: the child owns from turn 7, so the lineage says they
	// share everything below it.
	plan := in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 7)
	if plan.from > 6 {
		t.Fatalf("the seed would begin at turn %d and step over the open turn the clone dropped", plan.from)
	}
	if first, ok := in.lt.client.FirstMissing(); !ok || first != 6 {
		t.Fatalf("the clone reports its first missing turn as %d (%v), want 6", first, ok)
	}
}

// A HOLE THE STREAM OPENED IS CHASED WITHOUT A KEYSTROKE.
//
// The page pump ran only at the end of an input chunk, so a gap that arrived
// on a live frame was never asked about: 27068b2c watched one sit on screen
// for two seconds, and then G cleared it in a single read. The two halves of
// the same law: a fillable hole is filled ONCE, and once is not zero.
//
// IT DRIVES THE NOTIFY HANDLER AND COUNTS WHAT REACHES THE WIRE. An earlier
// version of this test asserted only that the pager could SEE the hole, which
// is a predicate and not the behaviour: it passed with the scheduling deleted.
// Caught in review by 27068b2c.
func TestHop_AGapFromTheStreamIsChasedWithoutAKey(t *testing.T) {
	var out bytes.Buffer
	reads := make(chan aria.Anchor, 8)
	in := &interactiveInput{mu: &sync.Mutex{}}
	in.fcli = &countingReader{reads: reads}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.setHistoryFetcher(in.historyFetcher())
	in.lt.apply(aria.Page{Parts: partsFor(1, 3, "PARENT")})
	in.lt.tr.buildIndex()

	handler := in.notifyHandler(in.subjectGeneration())
	frame, err := json.Marshal(aria.Page{
		Parts: partsFor(9, 1, "LATER"),
		More:  aria.More{Before: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A turn nine turns above the window arrives on the stream. Nobody
	// touches the keyboard.
	handler(rpc.MethodAriaFrame, frame)

	select {
	case at := <-reads:
		if at.Turn == 0 {
			t.Fatalf("the chase asked at the zero anchor, which this wire reads as the tail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream opened a hole and nothing asked the wire about it")
	}
}

// countingReader reports every history read the pager makes, with the anchor
// it asked at.
type countingReader struct{ reads chan aria.Anchor }

func (c *countingReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (c *countingReader) ReadBefore(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	select {
	case c.reads <- at:
	default:
	}
	// Answer with the turns between what is held and what arrived, so the hole
	// closes and the chase does not repeat: this test is about the first ask,
	// and TestHop_AnIdlePagerAsksForNothing is about there not being a second.
	return aria.Page{Parts: partsFor(4, 5, "FILL")}, nil
}

func (c *countingReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}
