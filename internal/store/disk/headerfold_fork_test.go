package disk

import (
	"testing"
)

// The hazard test for folding from a header. A consumer that pairs
// HeaderAt (which walks the parent chain) with SegmentBaseIndexes (which
// does not) folds the parent's header onto a range computed from the
// child's boundaries, silently, for any index below the fork point. This
// pins that, and the property any exposure must satisfy: FOLD(header-of-idx,
// records[base-of-idx .. idx]) == StateAt(idx) at every index, on a trunk
// and across a fork.

// headerFoldAt is the naive pairing: ask for the header, then find the base
// by searching this log's own segment bases for the greatest one <= idx.
func headerFoldAt(t *testing.T, l *Log, idx uint64) ([]byte, error) {
	t.Helper()
	header, err := l.HeaderAt(idx)
	if err != nil {
		return nil, err
	}
	base := uint64(1)
	for _, b := range l.SegmentBaseIndexes() {
		if b <= idx && b > base {
			base = b
		}
	}
	sealed := make([][]byte, 0, idx-base+1)
	for i := base; i <= idx; i++ {
		p, err := l.Read(i)
		if err != nil {
			return nil, err
		}
		sealed = append(sealed, p)
	}
	return sumFold(header, sealed)
}

// The control: on a log with no parent the naive pairing reproduces StateAt
// at every index. If this fails, the fork test below proves nothing.
func TestHeaderFold_OnATrunkTheNaivePairingIsCorrect(t *testing.T) {
	dir := t.TempDir()
	l := openReducible(t, dir, 256) // small, so it rotates
	defer l.Close()

	const n = 40
	for i := uint64(1); i <= n; i++ {
		if err := l.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// POSITIVE ASSERTION: a single-segment log would make this test vacuous,
	// because the header would always be the initial one.
	if got := len(l.SegmentBaseIndexes()); got < 3 {
		t.Fatalf("fixture did not rotate enough: %d segments, want >= 3", got)
	}

	for idx := uint64(1); idx <= n; idx++ {
		want, err := l.StateAt(idx)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", idx, err)
		}
		got, err := headerFoldAt(t, l, idx)
		if err != nil {
			t.Fatalf("headerFoldAt(%d): %v", idx, err)
		}
		if deU64(got) != deU64(want) {
			t.Fatalf("idx %d: header-fold %d, StateAt %d", idx, deU64(got), deU64(want))
		}
	}
}

// The hazard. Adding SegmentHeaderAt does not make this false: HeaderAt
// still walks the parent chain, SegmentBaseIndexes still does not, and
// pairing them is still wrong below a fork base. Whoever makes this test go
// red has changed one of those two functions and owes the design that
// depends on the difference a look.
func TestHeaderFold_AcrossAForkTheNaivePairingIsWrong(t *testing.T) {
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

	// Fork PAST several segment boundaries, so there are indices below the
	// fork base that live in the parent's EARLIER segments -- the ones the
	// child knows nothing about.
	const cut = 30
	child, err := parent.Fork(cut, "branch")
	if err != nil {
		parent.Close()
		t.Fatal(err)
	}
	defer child.Close()
	defer parent.Close()

	if child.ForkBase() != cut {
		t.Fatalf("forkBase = %d, want %d", child.ForkBase(), cut)
	}

	// Write a little on the branch so it has segments of its own.
	for i := uint64(cut); i < cut+8; i++ {
		if err := child.Write(i, u64(i)); err != nil {
			t.Fatalf("child write %d: %v", i, err)
		}
	}

	// THE TWO FACTS DISAGREE ABOUT WHICH LINEAGE THEY DESCRIBE.
	bases := child.SegmentBaseIndexes()
	for _, b := range bases {
		if b < cut {
			t.Fatalf("child reported a segment base %d below its fork base %d, "+
				"which would mean SegmentBaseIndexes walks the parent after all "+
				"and this whole test is about a hazard that does not exist", b, cut)
		}
	}

	// Below the fork base the naive pairing is WRONG, and this asserts it is
	// wrong so that fixing it is visible.
	wrong := 0
	for idx := uint64(1); idx < cut; idx++ {
		want, err := child.StateAt(idx)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", idx, err)
		}
		got, err := headerFoldAt(t, child, idx)
		if err != nil {
			// An error is an honest outcome too -- it is not a silent wrong
			// answer. Count it as "not wrong" and let the assertion below
			// speak for the ones that returned a number.
			continue
		}
		if deU64(got) != deU64(want) {
			wrong++
		}
	}
	if wrong == 0 {
		t.Fatal("EXPECTED THE NAIVE PAIRING TO PRODUCE WRONG STATE BELOW THE FORK " +
			"BASE, AND THIS IS NOT A TEST THAT WANTS FIXING. Pairing HeaderAt " +
			"(which walks the parent chain) with SegmentBaseIndexes (which does " +
			"not) is wrong below a fork base and REMAINS wrong; that is the " +
			"standing reason disk.Log.SegmentHeaderAt exists, and the correct " +
			"one-call route is asserted by TestSegmentHeaderAt_AcrossAFork" +
			"ItAgreesWhereThePairingDoesNot. If you are reading this, you have " +
			"changed HeaderAt or SegmentBaseIndexes -- most likely by making " +
			"SegmentBaseIndexes walk the parent chain -- and figaro's delta seam " +
			"depends on the difference between them. DO NOT INVERT OR DELETE this " +
			"test to make it green: go and look at what you changed.")
	}
	t.Logf("the naive pairing produced wrong state at %d of %d indices below the fork base",
		wrong, cut-1)

	// AT OR ABOVE the fork base the child's own bases apply, so the pairing
	// is correct there -- which is what makes the bug subtle rather than
	// obvious: it is right everywhere a single-lineage test would look.
	for idx := uint64(cut); idx < cut+8; idx++ {
		want, err := child.StateAt(idx)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", idx, err)
		}
		got, err := headerFoldAt(t, child, idx)
		if err != nil {
			t.Fatalf("headerFoldAt(%d): %v", idx, err)
		}
		if deU64(got) != deU64(want) {
			t.Fatalf("idx %d at/above the fork base should agree: header-fold %d, StateAt %d",
				idx, deU64(got), deU64(want))
		}
	}
}
