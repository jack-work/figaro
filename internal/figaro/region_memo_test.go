package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// The region memo must equal the full materialization, and composeTurn's
// append of the in-flight message must never land in the memo's backing array.

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

	for i := 0; i < 3; i++ {
		if got, want := a.regionMessages(), regionOracle(a); !sameMessages(want, got) {
			t.Fatalf("frame %d with no appends: memo has %d messages, oracle has %d", i, len(got), len(want))
		}
	}

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

	// A new turn replaces the region.
	a.turnStartLT = uint64(a.figLog.Len()) - 2
	got, want := a.regionMessages(), regionOracle(a)
	if !sameMessages(want, got) {
		t.Fatalf("after a turn boundary: memo has %d messages, oracle has %d\n"+
			"the memo was carried across a region change", len(got), len(want))
	}
}

// Aliasing is a capacity question: no content comparison can see it.
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

	before := len(a.regionMessages())
	_ = append(got, message.Message{Role: message.RoleOutput}) //nolint:gocritic // the caller's append, on purpose
	if after := len(a.regionMessages()); after != before {
		t.Fatalf("a caller's append changed the memo's length from %d to %d", before, after)
	}
}
