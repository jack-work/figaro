package disk

import (
	"testing"
)

// THE HAZARD TEST FOR FOLDING FROM A HEADER, WRITTEN BEFORE THE API THAT
// WOULD MAKE IT PASS.
//
// figaro's delta seam wants to build a snapshot the cheap way: take the
// block-0 header of the segment holding idx, and fold ONLY [segBase..idx]
// onto it. StateAt already does exactly that internally. What figaro cannot
// do is StateAt, because StateAt folds through the reducer -- and figaro's
// reducer JSON round-trips the entire board PER RECORD. It needs the header
// BYTES and then folds the records itself, decoded, once.
//
// TO DO THAT IT NEEDS TWO FACTS: the header, and THE BASE INDEX OF THE
// SEGMENT THAT HEADER BELONGS TO. The base is what says which records to
// fold. And the two facts are not equally available:
//
//	HeaderAt(idx)          WALKS THE PARENT CHAIN for idx < forkBase.
//	SegmentBaseIndexes()   "does NOT walk the parent chain" -- its own doc.
//
// SO ON A BRANCH, FOR ANY INDEX BELOW THE FORK POINT, PAIRING THEM IS WRONG
// AND SILENT: HeaderAt answers from the parent, SegmentBaseIndexes answers
// only about the child, and a caller that pairs them folds the parent's
// header onto a range computed from the child's boundaries. Nobody is told.
// The result is a wrong snapshot, only before a fork point.
//
// This file pins that, and pins the property any exposure must satisfy:
// FOLD(header-of-idx, records[base-of-idx .. idx]) == StateAt(idx), AT EVERY
// INDEX, ON A TRUNK AND ACROSS A FORK. StateAt is the oracle because it is
// the same computation done by the library that owns the segments.

// headerFoldAt is what a consumer would write given ONLY the two functions as
// they exist today: ask for the header, then find the base by searching this
// log's own segment bases for the greatest one <= idx. It is the naive
// pairing, written out so the test can show exactly where it goes wrong
// rather than describing it.
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

// TestHeaderFold_OnATrunkTheNaivePairingIsCorRect: the control. On a log with
// no parent, HeaderAt and SegmentBaseIndexes describe the same lineage, so
// the naive pairing reproduces StateAt at every index. If this fails, the
// test below proves nothing about forks -- it would only prove the harness
// is broken.
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

// TestHeaderFold_AcrossAForkTheNaivePairingIsWRONG is the hazard.
//
// AMENDED BY ba221ff1 AFTER SegmentHeaderAt LANDED, on d921742d's ruling.
// This test was written expecting to be INVERTED once an exposure handed
// the header and its base together. That instruction was wrong and is
// withdrawn: ADDING SegmentHeaderAt DOES NOT MAKE THIS FALSE. HeaderAt
// still walks the parent chain, SegmentBaseIndexes still does not, and
// pairing them is still wrong below a fork base -- and will REMAIN wrong
// unless someone changes one of those two functions, which this campaign
// is not doing. Inverting it would make it assert something untrue on the
// day it was inverted. Assert the fact, not the wish.
//
// SO IT STAYS, AND IT EARNS ITS KEEP TWICE. It is the STANDING REASON
// disk.Log.SegmentHeaderAt exists -- the positive side is
// TestSegmentHeaderAt_AcrossAForkItAgreesWhereThePairingDoesNot. And it is
// a TRIPWIRE: whoever makes this test go red has changed HeaderAt or
// SegmentBaseIndexes -- most likely by "tidying" SegmentBaseIndexes into
// walking the parent chain -- and owes the design that depends on that
// difference a look before landing it.
//
// THE MEASURED COUNT, which is the part an argument cannot replace: on a
// 40-entry reducible log forked at 30, the naive pairing produces WRONG
// STATE AT 14 OF 29 INDICES BELOW THE FORK BASE, and is correct at EVERY
// index at or above it. That is why a single-lineage test would have found
// nothing: the pairing is right everywhere such a test would look.
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
