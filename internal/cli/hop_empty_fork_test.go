package cli

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// exp9, v0.57.1: attending a fork made at turn 4 with no prompt of its own
// painted "the rest of this turn is not loaded" for good. The clone of the
// parent claims more above turn 4; the fork's tail read, floored at 5, is
// empty, and an empty page settled nothing.

type emptyAboveReader struct {
	top   int
	reads int
}

func (r *emptyAboveReader) Read(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	r.reads++
	if int(at.Turn) > r.top {
		return aria.Page{}, nil
	}
	return aria.Page{Parts: partsFor(int(at.Turn), r.top-int(at.Turn)+1, "FORK")}, nil
}

func (r *emptyAboveReader) ReadBefore(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	return r.ReadBeforeFloor(ctx, at, aria.Anchor{}, budget)
}

func (r *emptyAboveReader) ReadBeforeFloor(_ context.Context, _ aria.Anchor, floor aria.Anchor, _ int) (aria.Page, error) {
	r.reads++
	lo := max(int(floor.Turn), 1)
	if lo > r.top {
		return aria.Page{}, nil
	}
	return aria.Page{Parts: partsFor(lo, r.top-lo+1, "FORK"), More: aria.More{Before: lo > 1}}, nil
}

func (r *emptyAboveReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func hopOntoEmptyFork(t *testing.T, kind seedKind) (*interactiveInput, *frameTape) {
	t.Helper()
	tape := newFrameTape(80, 20)
	in := &interactiveInput{mu: new(renderLock)}
	in.fcli = &emptyAboveReader{top: 4}
	in.lt = newLivelogTurn(tape, 80, 20, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.tr.after = nil
	in.lt.apply(aria.Page{Parts: partsFor(1, 6, "PARENT")})
	in.lt.setMoreBefore(false)
	in.lt.tr.buildIndex()

	in.parkSubject("ariaAAAA")
	in.seed = in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 5)
	if !in.lt.client.MoreAfter() {
		t.Fatal("fixture: the clone is sure of its tail, so there is nothing to settle")
	}
	if kind != in.seed.kind {
		in.seed.kind = kind
	}
	in.seedSubject(in.subjectGeneration())
	return in, tape
}

func TestHop_AnEmptyForkPaintsNoHole(t *testing.T) {
	for _, kind := range []seedKind{seedTail, seedSuffix} {
		in, tape := hopOntoEmptyFork(t, kind)
		if in.lt.client.MoreAfter() {
			t.Errorf("seed %d: an empty read above the fork point left the store claiming more", kind)
		}
		in.lt.tr.render()
		if sentinelIn(tape.frames[len(tape.frames)-1:]) {
			t.Errorf("seed %d: the fork's last frame paints a hole:\n%v", kind, tape.frames[len(tape.frames)-1])
		}
	}
}

func TestHop_AnEmptyForkAsksTheWireNothingMore(t *testing.T) {
	in, _ := hopOntoEmptyFork(t, seedTail)
	r := in.fcli.(*emptyAboveReader)
	r.reads = 0
	for range 20 {
		in.lt.tr.buildIndex()
		if req, want := in.lt.tr.pageCursor(); want {
			t.Fatalf("the pager still wants a page on a fork it holds whole: %+v", req)
		}
	}
}
