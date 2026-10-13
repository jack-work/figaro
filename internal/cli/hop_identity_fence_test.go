package cli

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// A dialled connection streams on accept, before claimSubject swaps the
// client; a frame arriving in that gap belongs to the branch, not the parent.
func TestHop_AFrameFromTheAriaBeingDialledNeverReachesTheOneBeingLeft(t *testing.T) {
	in := fenceSession(t)
	const parent, branch = "parentAA", "branchBB"

	gen := atomic.AddUint64(&in.subjectGen, 1)
	if _, _, ok := in.claimSubject(gen, parent, "", nil, 0); !ok {
		t.Fatal("could not claim the parent")
	}
	in.lt.apply(aria.Page{Parts: partsFor(1, 3, "PARENT")})

	// A new generation, dialled, not yet claimed.
	gen = atomic.AddUint64(&in.subjectGen, 1)
	handler := in.notifyHandler(gen, branch)

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

	// The parent must go on the shelf clean.
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

	handler(rpc.MethodAriaFrame, frame)
	if _, ok := in.lt.client.InquiryOf(4); !ok {
		t.Fatal("after the claim the branch's own question was dropped: the fence is too wide")
	}
}

// The id check is added to the generation fence, not substituted for it.
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
