package disk

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

// The positive side of headerfold_fork_test.go: the one-call route is right
// at every index, on a trunk, across a fork, across two forks, and on a
// frozen branch point. The oracle is StateAt.
//
// The value oracle works here only because sumFold is not idempotent: for
// an idempotent reducer a too-low base re-applies records the header
// already contains and the value is identical. If sumFold is ever replaced
// with an idempotent reducer, check the returned base independently, as
// xwal/segmentheaderat_test.go does.

// foldFromSegmentHeader is what a consumer writes given the NEW call: ask for
// the header and its base together, then fold the records in [base..idx] onto
// the header. Read walks the parent chain, so a parent-owned range reads fine
// through the child.
func foldFromSegmentHeader(t *testing.T, l *Log, idx uint64) ([]byte, uint64, error) {
	t.Helper()
	header, base, err := l.SegmentHeaderAt(idx)
	if err != nil {
		return nil, 0, err
	}
	if base == 0 {
		t.Fatalf("SegmentHeaderAt(%d) returned base 0 with a nil error: a base is "+
			"1-origin, so 0 can only mean 'unknown', and an unknown answer must be "+
			"an error and never a value", idx)
	}
	if base > idx {
		t.Fatalf("SegmentHeaderAt(%d) returned base %d ABOVE the requested index: "+
			"the segment does not contain idx", idx, base)
	}
	sealed := make([][]byte, 0, idx-base+1)
	for i := base; i <= idx; i++ {
		p, err := l.Read(i)
		if err != nil {
			return nil, 0, err
		}
		sealed = append(sealed, p)
	}
	got, err := sumFold(header, sealed)
	return got, base, err
}

// naiveBaseFor is the base the two-call route would compute, so the fork
// tests can assert that they exercise indices where the two routes disagree.
func naiveBaseFor(l *Log, idx uint64) uint64 {
	base := uint64(1)
	for _, b := range l.SegmentBaseIndexes() {
		if b <= idx && b > base {
			base = b
		}
	}
	return base
}

func assertFoldsMatchStateAt(t *testing.T, l *Log, lo, hi uint64) {
	t.Helper()
	for idx := lo; idx <= hi; idx++ {
		want, err := l.StateAt(idx)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", idx, err)
		}
		got, _, err := foldFromSegmentHeader(t, l, idx)
		if err != nil {
			t.Fatalf("foldFromSegmentHeader(%d): %v", idx, err)
		}
		if deU64(got) != deU64(want) {
			t.Fatalf("idx %d: fold-from-SegmentHeaderAt %d, StateAt %d",
				idx, deU64(got), deU64(want))
		}
	}
}

// The control: on a log with no parent both routes describe the same
// lineage.
func TestSegmentHeaderAt_OnATrunkItAgreesWithStateAt(t *testing.T) {
	dir := t.TempDir()
	l := openReducible(t, dir, 256)
	defer l.Close()

	const n = 40
	for i := uint64(1); i <= n; i++ {
		if err := l.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := len(l.SegmentBaseIndexes()); got < 3 {
		t.Fatalf("fixture did not rotate enough: %d segments, want >= 3", got)
	}
	assertFoldsMatchStateAt(t, l, 1, n)
}

// The hazard, answered: 40 entries forked at 30, so there are indices below
// the fork base living in the parent's earlier segments.
func TestSegmentHeaderAt_AcrossAForkItAgreesWhereThePairingDoesNot(t *testing.T) {
	dir := t.TempDir()
	parent := openReducible(t, dir, 256)

	const n = 40
	for i := uint64(1); i <= n; i++ {
		if err := parent.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := len(parent.SegmentBaseIndexes()); got < 3 {
		t.Fatalf("fixture did not rotate enough: %d segments, want >= 3", got)
	}

	const cut = 30
	child, err := parent.Fork(cut, "branch")
	if err != nil {
		parent.Close()
		t.Fatal(err)
	}
	defer child.Close()
	defer parent.Close()

	for i := uint64(cut); i < cut+8; i++ {
		if err := child.Write(i, u64(i)); err != nil {
			t.Fatalf("child write %d: %v", i, err)
		}
	}

	// THE PRIMARY CLAIM FIRST: the one-call route reproduces StateAt at every
	// index, above and below the fork base.
	assertFoldsMatchStateAt(t, child, 1, cut+7)

	// And the positive assertion that the hazard is reached: at some index
	// below the fork base the one-call base must differ from the two-call
	// base, or a green run could mean the two routes happen to agree.
	disagreed := 0
	for idx := uint64(1); idx < cut; idx++ {
		_, base, err := foldFromSegmentHeader(t, child, idx)
		if err != nil {
			t.Fatalf("foldFromSegmentHeader(%d): %v", idx, err)
		}
		if base != naiveBaseFor(child, idx) {
			disagreed++
		}
		if base >= cut {
			t.Fatalf("idx %d below the fork base %d got base %d, which is the "+
				"CHILD's lineage: the parent chain was not walked", idx, cut, base)
		}
	}
	if disagreed == 0 {
		t.Fatal("the one-call base never differed from the two-call base below the " +
			"fork point, so this fixture does not reach the hazard it exists for")
	}
	t.Logf("the one-call base differed from the naive pairing at %d of %d indices "+
		"below the fork base", disagreed, cut-1)
}

// HeaderAt recurses, and a one-level fixture cannot tell a correct walk from
// one that stops after a single hop: this forks twice and reads an index
// owned by the grandparent through the grandchild.
func TestSegmentHeaderAt_TwoLevelsOfForkGrandchildReadsGrandparent(t *testing.T) {
	dir := t.TempDir()
	gp := openReducible(t, dir, 256)

	const n = 40
	for i := uint64(1); i <= n; i++ {
		if err := gp.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := len(gp.SegmentBaseIndexes()); got < 3 {
		t.Fatalf("fixture did not rotate enough: %d segments, want >= 3", got)
	}

	const cut1 = 30
	child, err := gp.Fork(cut1, "child")
	if err != nil {
		gp.Close()
		t.Fatal(err)
	}
	defer gp.Close()
	for i := uint64(cut1); i < cut1+10; i++ {
		if err := child.Write(i, u64(i)); err != nil {
			child.Close()
			t.Fatalf("child write %d: %v", i, err)
		}
	}
	const cut2 = 36
	grand, err := child.Fork(cut2, "grand")
	if err != nil {
		child.Close()
		t.Fatal(err)
	}
	defer grand.Close()
	defer child.Close()
	for i := uint64(cut2); i < cut2+6; i++ {
		if err := grand.Write(i, u64(i)); err != nil {
			t.Fatalf("grand write %d: %v", i, err)
		}
	}

	if grand.ForkBase() != cut2 || child.ForkBase() != cut1 {
		t.Fatalf("fork bases: grand %d (want %d), child %d (want %d)",
			grand.ForkBase(), cut2, child.ForkBase(), cut1)
	}

	// THE TWO-HOP INDEX: below the CHILD's fork base, so answering it
	// requires grand -> child -> grandparent. A walk that stops after one
	// hop lands in the child, which owns nothing down here.
	for idx := uint64(1); idx < cut1; idx++ {
		_, base, err := foldFromSegmentHeader(t, grand, idx)
		if err != nil {
			t.Fatalf("foldFromSegmentHeader(%d): %v", idx, err)
		}
		if base >= cut1 {
			t.Fatalf("idx %d is owned by the GRANDPARENT (below the child's fork "+
				"base %d) but the base came back as %d: the walk stopped short",
				idx, cut1, base)
		}
	}
	// POSITIVE ASSERTION that the middle level is real: an index between the
	// two cuts is owned by the CHILD, and must resolve there, not lower.
	for idx := uint64(cut1); idx < cut2; idx++ {
		_, base, err := foldFromSegmentHeader(t, grand, idx)
		if err != nil {
			t.Fatalf("foldFromSegmentHeader(%d): %v", idx, err)
		}
		if base < cut1 {
			t.Fatalf("idx %d is owned by the CHILD but resolved to base %d, below "+
				"the child's fork base %d: the walk went one hop too far",
				idx, base, cut1)
		}
	}

	assertFoldsMatchStateAt(t, grand, 1, cut2+5)
}

// A fork makes its parent read-only at the split, so a frozen log's segments
// are all sealed, and a lookup that leans on the active segment's shape
// answers differently there.
func TestSegmentHeaderAt_OnAFrozenBranchPoint(t *testing.T) {
	dir := t.TempDir()
	parent := openReducible(t, dir, 256)
	defer parent.Close()

	const n = 40
	for i := uint64(1); i <= n; i++ {
		if err := parent.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	const cut = 30
	child, err := parent.Fork(cut, "branch")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if !parent.IsReadOnly() {
		t.Fatal("the branch point is not read-only, so this test is asserting " +
			"nothing about a frozen log")
	}
	// The frozen log keeps its whole range readable: [1..cut-1] survives the
	// split, and above the split the records moved to the old-future.
	assertFoldsMatchStateAt(t, parent, 1, cut-1)
}

// Three distinct refusals, because a caller must never read base == 0 as
// "unknown". A nil header beside a valid base folds to a plausible
// from-empty state, so a log with no headers is an error here even though
// HeaderAt answers nil for it.
func TestSegmentHeaderAt_AMissNeverALie(t *testing.T) {
	t.Run("not in header mode", func(t *testing.T) {
		l, err := Open(t.TempDir(), Options{
			Codec:       segment.BinaryCodec{},
			SegmentSize: 256,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if err := l.Write(1, u64(1)); err != nil {
			t.Fatal(err)
		}
		// The contrast that makes this a ruling and not a preference:
		// HeaderAt answers nil, nil -- a caller pairing that with a base
		// gets a plausible from-empty state and is never told.
		h, herr := l.HeaderAt(1)
		if herr != nil || h != nil {
			t.Fatalf("HeaderAt on a header-less log = (%v, %v), want (nil, nil): "+
				"if this changed, SegmentHeaderAt's doc comment is now wrong", h, herr)
		}
		_, _, err = l.SegmentHeaderAt(1)
		if !errors.Is(err, ErrNotHeaderMode) {
			t.Fatalf("SegmentHeaderAt on a header-less log: err = %v, want ErrNotHeaderMode", err)
		}
	})

	t.Run("empty log", func(t *testing.T) {
		l := openReducible(t, t.TempDir(), 256)
		defer l.Close()
		_, _, err := l.SegmentHeaderAt(1)
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("SegmentHeaderAt on an empty log: err = %v, want ErrEmpty", err)
		}
	})

	t.Run("index not found", func(t *testing.T) {
		l := openReducible(t, t.TempDir(), 256)
		defer l.Close()
		for i := uint64(1); i <= 5; i++ {
			if err := l.Write(i, u64(i)); err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := l.SegmentHeaderAt(99)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("SegmentHeaderAt past the tail: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("the three are distinct", func(t *testing.T) {
		if errors.Is(ErrNotHeaderMode, ErrEmpty) || errors.Is(ErrNotHeaderMode, ErrNotFound) ||
			errors.Is(ErrEmpty, ErrNotFound) {
			t.Fatal("the three refusals are not distinguishable, so a caller cannot " +
				"tell 'this log has no headers' from 'this index is not here'")
		}
	})
}
