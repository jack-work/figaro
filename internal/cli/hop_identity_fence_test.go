package cli

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ISSUE #25, AT THE SEAM WHERE IT LIVED.
//
// The pager attends a parent. `:fork -- test` prompts a branch and then hops
// to it. The hop takes its generation, dials the branch (which is subscribed
// on accept and starts streaming at once), and only then waits for the lineage
// read before claimSubject swaps the client. The branch's opening question,
// an OpenInquiry frame for the turn after the parent's last, arrives in that
// gap. It passed the generation check and was folded into the PARENT's
// client, which claimSubject then shelved; coming back to the parent restored
// it from the shelf and painted the branch's question at the end of the
// parent's reply.
//
// Captured on a real tape (Gluck, 2026-10-10): the turn-10 frame at t=66.578,
// beside form deltas naming the branch, before the client's first read for it
// at t=66.617. This drives the same order without a wire.
func TestHop_AFrameFromTheAriaBeingDialledNeverReachesTheOneBeingLeft(t *testing.T) {
	in := fenceSession(t)
	const parent, branch = "parentAA", "branchBB"

	// The parent is on screen, claimed, with three turns.
	gen := atomic.AddUint64(&in.subjectGen, 1)
	if _, _, ok := in.claimSubject(gen, parent, "", nil, 0); !ok {
		t.Fatal("could not claim the parent")
	}
	in.lt.apply(aria.Page{Parts: partsFor(1, 3, "PARENT")})

	// The hop to the branch begins: a new generation, a connection dialled for
	// the branch with its handler, and no claim yet.
	gen = atomic.AddUint64(&in.subjectGen, 1)
	handler := in.notifyHandler(gen, branch)

	// The branch opens its first turn: the question alone, as
	// Server.OpenInquiry broadcasts it.
	frame, err := json.Marshal(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 4, Inquiry: "test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler(rpc.MethodAriaFrame, frame)

	if inq, ok := in.lt.client.InquiryOf(4); ok {
		t.Fatalf("the branch's question %q landed in the parent's client before the hop claimed the branch", inq.Text)
	}
	if turn, _, ok := in.lt.client.OpenKey(); ok && turn == 4 {
		t.Fatal("the parent's client holds the branch's turn 4 open")
	}

	// The claim: the parent goes on the shelf, and must go there clean.
	if _, _, ok := in.claimSubject(gen, branch, parent, nil, 3); !ok {
		t.Fatal("could not claim the branch")
	}
	shelved := in.parked[parent]
	if shelved == nil {
		t.Fatal("the parent was not shelved")
	}
	if _, ok := shelved.client.InquiryOf(4); ok {
		t.Fatal("the parent went onto the shelf carrying the branch's question: the hop back will paint it")
	}

	// After the claim the same frame is the branch's own, and is folded.
	handler(rpc.MethodAriaFrame, frame)
	if _, ok := in.lt.client.InquiryOf(4); !ok {
		t.Fatal("after the claim the branch's own question was dropped: the fence is too wide")
	}
}

// A connection whose switch was SUPERSEDED stays mute even if its aria is
// later claimed by another switch: the generation still fences it. The id
// check is added to the generation, it does not replace it.
func TestHop_TheIdentityFenceDoesNotReplaceTheGeneration(t *testing.T) {
	in := fenceSession(t)
	stale := atomic.AddUint64(&in.subjectGen, 1)
	old := in.notifyHandler(stale, "ariaCCCC")
	fresh := atomic.AddUint64(&in.subjectGen, 1)
	if _, _, ok := in.claimSubject(fresh, "ariaCCCC", "", nil, 0); !ok {
		t.Fatal("could not claim")
	}
	frame, _ := json.Marshal(aria.Page{Parts: partsFor(1, 1, "STALE")})
	old(rpc.MethodAriaFrame, frame)
	if in.lt.client.Count() != 0 {
		t.Fatal("a connection from a superseded switch folded a frame")
	}
}
