package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"

	"github.com/jack-work/figaro/sdk/aria"
)

// A hole the wire cannot fill must not be asked about on every frame.
func TestHop_AnIdlePagerAsksForNothing(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT")})
	in.lt.setMoreBefore(false)
	in.lt.tr.buildIndex()

	// The clone keeps turns 1 and 2 and claims more above; the child's tail
	// page says there is not.
	in.parkSubject("ariaAAAA")
	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 3)
	in.lt.apply(aria.Page{Parts: partsFor(3, 2, "BRANCH"), More: aria.More{Before: true}})
	in.lt.tr.buildIndex()

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

// Folding a page is the only thing that corrects a store's belief about what
// lies above it, so a parked store that says "more above" owes one read.
func TestHop_AdoptingAnUnsureTailOwesOneRead(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
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

// The lineage's shared floor is an upper bound: the clone drops the open
// turn, so a read floored at the fork point steps over it.
func TestHop_AForkOffALiveTurnLeavesNoHole(t *testing.T) {
	var out bytes.Buffer
	in := &interactiveInput{}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 5, "PARENT")})
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

	plan := in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 7)
	if plan.from > 6 {
		t.Fatalf("the seed would begin at turn %d and step over the open turn the clone dropped", plan.from)
	}
	if first, ok := in.lt.client.FirstMissing(); !ok || first != 6 {
		t.Fatalf("the clone reports its first missing turn as %d (%v), want 6", first, ok)
	}
}

// A gap opened by a live frame is chased without a keystroke; the assertion
// is what reaches the wire, not whether the pager can see the hole.
func TestHop_AGapFromTheStreamIsChasedWithoutAKey(t *testing.T) {
	var out bytes.Buffer
	reads := make(chan aria.Anchor, 8)
	in := &interactiveInput{mu: new(renderLock)}
	in.fcli = &countingReader{reads: reads}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.setHistoryFetcher(in.historyFetcher())
	in.lt.apply(aria.Page{Parts: partsFor(1, 3, "PARENT")})
	in.lt.tr.buildIndex()

	handler := in.notifyHandler(in.subjectGeneration(), in.currentID())
	frame, err := json.Marshal(aria.Page{
		Parts: partsFor(9, 1, "LATER"),
		More:  aria.More{Before: true},
	})
	if err != nil {
		t.Fatal(err)
	}

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

type countingReader struct{ reads chan aria.Anchor }

func (c *countingReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (c *countingReader) ReadBefore(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	select {
	case c.reads <- at:
	default:
	}
	// Close the hole so the chase does not repeat.
	return aria.Page{Parts: partsFor(4, 5, "FILL")}, nil
}

func (c *countingReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}
