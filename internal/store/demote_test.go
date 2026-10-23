package store_test

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func presentOf(t *testing.T, b *store.XwalBackend) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range b.Nodes() {
		out[n.ID] = n.Present
	}
	return out
}

func withTurns(t *testing.T, b *store.XwalBackend, id string, n int) {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}}); err != nil {
			t.Fatal(err)
		}
	}
}

// Three branches of one aria, cut at different points.
func demoteFixture(t *testing.T) (b *store.XwalBackend, parent string, early, mid, late string) {
	t.Helper()
	b, outfit := ttlPresentBackend(t)
	parent, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	withTurns(t, b, parent, 6)
	fork := func(at uint64) string {
		_, alt, err := b.ForkAt(parent, at)
		if err != nil {
			t.Fatalf("fork at %d: %v", at, err)
		}
		return alt
	}
	mid = fork(4)
	early = fork(3)
	late = fork(6)
	return b, parent, early, mid, late
}

// The point of demote: afterwards the demoted aria is a leaf, so deleting it
// without -r is allowed and every branch survives, readable.
func TestDemotedAriaDeletesAloneAndBranchesSurvive(t *testing.T) {
	b, parent, early, mid, late := demoteFixture(t)
	was := presentOf(t, b)[parent]

	under, err := b.Demote(parent, mid)
	if err != nil {
		t.Fatal(err)
	}
	if under != mid {
		t.Fatalf("demote went under %s, want the preferred %s", under, mid)
	}
	p := presentOf(t, b)
	if p[mid] != was {
		t.Errorf("preferred child drawn under %q, want the demoted aria's old place %q", p[mid], was)
	}
	for _, id := range []string{early, late, parent} {
		if p[id] != mid {
			t.Errorf("%s drawn under %q, want %s", id, p[id], mid)
		}
	}

	if err := b.Remove(parent, false); err != nil {
		t.Fatalf("a demoted aria is a leaf; a plain delete must succeed: %v", err)
	}
	live := ttlIDs(b)
	for _, id := range []string{early, mid, late} {
		if !live[id] {
			t.Fatalf("branch %s was taken with the demoted aria", id)
		}
		if _, err := b.OpenFigIR(id); err != nil {
			t.Errorf("branch %s unreadable after the delete: %v", id, err)
		}
	}
}

func TestDemoteWithoutPreferencePicksTheEarliestBranch(t *testing.T) {
	b, parent, early, _, _ := demoteFixture(t)
	under, err := b.Demote(parent, "")
	if err != nil {
		t.Fatal(err)
	}
	if under != early {
		t.Fatalf("demote went under %s, want the earliest branch %s", under, early)
	}
}

// A preference that is not a child is ignored, not an error: succeed passes
// its successor whether or not it landed under the old aria.
func TestDemoteIgnoresAPreferenceThatIsNotAChild(t *testing.T) {
	b, parent, early, _, _ := demoteFixture(t)
	under, err := b.Demote(parent, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if under != early {
		t.Fatalf("demote went under %s, want %s", under, early)
	}
}

func TestDemotingALeafIsANoOp(t *testing.T) {
	b, outfit := ttlPresentBackend(t)
	leaf, _ := b.CreateConversation(outfit)
	before := presentOf(t, b)
	under, err := b.Demote(leaf, "")
	if err != nil || under != "" {
		t.Fatalf("Demote(leaf) = %q, %v; want \"\", nil", under, err)
	}
	if after := presentOf(t, b); after[leaf] != before[leaf] {
		t.Fatalf("a leaf moved: %q -> %q", before[leaf], after[leaf])
	}
}

func TestDemoteUnknownAriaFails(t *testing.T) {
	b, _ := ttlPresentBackend(t)
	if _, err := b.Demote("deadbeef", ""); err == nil {
		t.Fatal("demoting an unknown aria must fail")
	}
}
