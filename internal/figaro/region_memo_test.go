package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// THE HAZARD, pinned before the memo that could violate it.
//
// composeTurn is about to stop re-reading the whole open region every frame
// and hold it instead, extending it by what the log appended since. Two ways
// that goes wrong, and neither is slow -- both are wrong:
//
//  1. THE MEMO GOES STALE: a message appended between frames, or a new turn's
//     region, served from the previous turn's memo. The oracle is the full
//     materialization, kept permanently.
//  2. THE MEMO IS MUTATED UNDER A READER: composeTurn appends the in-flight
//     message to the region. If that append lands in the memo's own backing
//     array, every previously returned slice sharing it is edited in place --
//     the held-view law, pinned twice in internal/store, broken one package
//     over. This one cannot be seen by comparing contents at all; it is a
//     capacity question, so the test asks a capacity question.

func regionOracle(a *Agent) []message.Message {
	entries := a.figLog.ReadFrom(a.turnStartLT+1, 0)
	out := make([]message.Message, 0, len(entries))
	for _, e := range entries {
		m := e.Payload
		m.LogicalTime = e.LT
		out = append(out, m)
	}
	return out
}

func sameMessages(a, b []message.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].LogicalTime != b[i].LogicalTime || a[i].Role != b[i].Role ||
			len(a[i].Content) != len(b[i].Content) {
			return false
		}
		for j := range a[i].Content {
			if a[i].Content[j].Text != b[i].Content[j].Text ||
				a[i].Content[j].Type != b[i].Content[j].Type {
				return false
			}
		}
	}
	return true
}

func TestRegionMemoEqualsAFullMaterializationAtEveryFrame(t *testing.T) {
	a := openRegionAgent(4, 20, true)

	// Frames with nothing new: the memo must still equal the oracle.
	for i := 0; i < 3; i++ {
		if got, want := a.regionMessages(), regionOracle(a); !sameMessages(want, got) {
			t.Fatalf("frame %d with no appends: memo has %d messages, oracle has %d", i, len(got), len(want))
		}
	}

	// A message appended between frames must appear.
	for i := 0; i < 3; i++ {
		if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleOutput,
			Content: []message.Content{message.TextContent("appended between frames")},
		}}); err != nil {
			t.Fatal(err)
		}
		got, want := a.regionMessages(), regionOracle(a)
		if !sameMessages(want, got) {
			t.Fatalf("after append %d: memo has %d messages, oracle has %d", i, len(got), len(want))
		}
	}

	// A NEW TURN replaces the region. A memo carried across that boundary
	// would serve the previous turn's messages under the new turn's LTs.
	a.turnStartLT = uint64(a.figLog.Len()) - 2
	got, want := a.regionMessages(), regionOracle(a)
	if !sameMessages(want, got) {
		t.Fatalf("after a turn boundary: memo has %d messages, oracle has %d\n"+
			"the memo was carried across a region change", len(got), len(want))
	}
}

// THE ALIASING TRAP, asked as a capacity question because no content
// comparison can see it. composeTurn appends the in-flight message to what
// regionMessages returns; if that append fits in the memo's backing array, it
// overwrites a slot a previously returned slice may still be read through.
func TestRegionMemoCannotBeExtendedInPlace(t *testing.T) {
	a := openRegionAgent(4, 20, true)
	got := a.regionMessages()
	if len(got) == 0 {
		t.Fatal("the region is empty; this fixture cannot show aliasing")
	}
	if cap(got) != len(got) {
		t.Fatalf("regionMessages returned a slice with spare capacity (len %d, cap %d):\n"+
			"composeTurn appends the in-flight message to it, and that append would land in\n"+
			"the memo's own array, editing a slice an earlier frame may still be read through.\n"+
			"Return it capped (s[:n:n]) so the append is forced to copy.", len(got), cap(got))
	}

	// And the memo must survive the caller doing exactly that.
	before := len(a.regionMessages())
	_ = append(got, message.Message{Role: message.RoleOutput}) //nolint:gocritic // the caller's append, on purpose
	if after := len(a.regionMessages()); after != before {
		t.Fatalf("a caller's append changed the memo's length from %d to %d", before, after)
	}
}
