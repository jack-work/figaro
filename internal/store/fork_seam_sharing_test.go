package store

// THE FORK-SEAM SHARING MEASUREMENT.
//
// PRE-REGISTERED AT 44c82ab6, plans/fork-seam-sharing-prereg.md, BEFORE this
// file existed. Predictions, falsifier, canary and limits are there and are not
// restated here so that the two cannot drift apart.
//
// THE QUESTION: not how much ONE cache retains (a total, answered at 1.19x by
// b9925789) but HOW MUCH OF A FORK'S RESIDENT PREFIX IS SHARED WITH ITS PARENT
// -- what a fork's residency costs that the parent has ALREADY PAID.
//
// THE METHOD is 9ed3f561's keep-versus-drop heap delta turned sideways: the
// same fixture measured twice, once with the parent ALIVE and once with the
// parent DROPPED. It counts BYTES, so it does not move with load and needs
// neither a quiet box nor the bench lock.
//
// THIS INSTRUMENT FAILED, AND IT IS COMMITTED FAILING ON PURPOSE (503b9650's
// evidence, f3aa1d0b's ruling): the diff to the commit that fixes it is the
// correction, and a fixture that measures its own harness is worth seeing once.
//
//	A  child marginal cost, PARENT ALIVE  = 204,912 B
//	B  child cost, PARENT DROPPED         = 204,912 B   IDENTICAL TO THE BYTE
//	CANARY: seed ACCEPTED = 204,896 B; seed REJECTED = 112 B
//
// TWO NUMBERS AGREEING TO THE BYTE ARE A SUSPECT, NOT A FINDING, and the cause
// is below this comment rather than in the code under test: BOTH ARMS READ ONE
// MemLog THAT THE HARNESS KEEPS ALIVE, and a MemLog retains every decoded
// record forever. The payload strings are therefore held by the fixture in both
// arms, dropping the parent frees nothing, and B measures the same object A
// does. The precondition the whole experiment rests on -- that dropping the
// parent leaves the child the sole owner of those strings -- is violated by the
// harness itself. The fix is a disk-backed log, where the caches are the only
// decoded holders.
//
// THE CANARY FIRED IN THE WRONG DIRECTION, AND THAT PART IS ABOUT THE CODE.
// Spoiling the seed was predicted to make the child DECODE its own copy, so the
// marginal cost would RISE. It FELL, three orders of magnitude, because
// newWindowedLog PRE-READS NOTHING when window and budget are both zero: the
// eager tail read is guarded by `budget > 0 || window > 0`. So newSeededLog's
// documented degradation is not "decode a copy now", it is "RETAIN NOTHING
// UNTIL SOMEONE READS". That is a property of the constructor that nothing else
// in the tree asserts, and it is why this file is committed red rather than
// deleted.
//
// WHAT SURVIVES AND IS NOT IN DOUBT: on the ACCEPTED path the child costs
// 204,912 B for a 1000-record donated prefix -- ~205 B per record against a
// 2,326 B mean record, i.e. ~8.8% of encoded size in Entry structs for a prefix
// whose payloads it shares. A FLOOR: raw byte payloads, typed SDK structs
// unmeasured.
//
// UNFINISHED, AND STOOD DOWN RATHER THAN ABANDONED (f3aa1d0b, 2026-08-19).
// Gluck decided the direction on other grounds -- one canonical tree-shaped
// cache, layer reduction, the actor loop as the serialization mechanism -- so
// this measurement no longer gates a decision, and a measurement that gates
// nothing is scope that has outlived its question. It is committed in the state
// it reached because the honest half-artifact is worth more than a clean
// absence, and the next hand may want the overlap number when the residency
// work reaches it.
//
// THE CORRECTION IT NEEDS, WRITTEN DOWN SO NOBODY HAS TO REDISCOVER IT:
// replace seamFixture's MemLog with a DISK-BACKED log -- a real XwalBackend on
// a temp root, its records appended and the caches built over
// newXwalLog rather than over an in-memory log. That is the whole fix. It makes
// the two caches the ONLY decoded holders, so dropping the parent can actually
// free the strings and arm B can differ from arm A. Everything else in this
// file -- the two arms, the falsifier, the frame discipline, the canary's
// intent -- survives that change unaltered.
//
// TWO THINGS THE NEXT HAND SHOULD NOT HAVE TO LEARN TWICE. The canary must
// force the NON-SHARING path in a way that still populates the child (spoiling
// the seed does not, because a 0/0 cache retains nothing until read). And
// figwal's segment payload cache sits BELOW both arms and holds raw frames
// under a global budget, so it adds a constant to both readings that this
// method cannot separate out.
//
// IT IS RED. This branch must not be merged as it stands: TestForkSeamSharing
// fails on the falsifier and TestForkSeamSharingCanary fails on the direction,
// and both failures are the record rather than a defect to be silenced.
//
// IT IS WHITE-BOX ON PURPOSE. XwalBackend memoises one handle per aria, so a
// caller dropping its reference frees nothing; measuring through the backend
// would measure the memo. The seam under test is newSeededLog's donation, and
// that is what this builds directly.

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// heapAfterGC is b9925789's reading, unchanged: three cycles then HeapAlloc.
func heapAfterGC() uint64 {
	var m runtime.MemStats
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// seamFixture builds n records of recordBytes each in a fresh MemLog. Scale is
// taken from the owner's real store rather than from taste: the census this
// aria ran measured translation channels at p99 2,463,337 B with a mean record
// of 2,326 B, so 1000 x 2326 is the p99 shape.
func seamFixture(t *testing.T, n, recordBytes int) *MemLog[message.Message] {
	t.Helper()
	inner := NewMemLog[message.Message]()
	for i := 0; i < n; i++ {
		blob := make([]byte, recordBytes)
		for j := range blob {
			blob[j] = 'x'
		}
		if _, err := inner.Append(Entry[message.Message]{
			Payload: message.Message{
				Role:    message.RoleInput,
				Content: []message.Content{message.TextContent(string(blob))},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return inner
}

// seamRun performs ONE keep-versus-drop measurement INSIDE ITS OWN FRAME and
// returns only numbers. The frame matters: a local still live in the caller
// keeps its object reachable, which is how a residency measurement ends up
// measuring the test's own stack (ad737f41 paid for that lesson).
//
// parentAlive=false drops the parent BEFORE the child is measured, which is the
// whole experiment: the same child, once as a marginal holder and once as the
// sole owner of the identical strings.
//
// spoilSeed drives newSeededLog's DOCUMENTED degradation to a decode. It is the
// canary: it must make the child stop sharing, and the marginal cost must rise
// to meet the sole-owner cost.
func seamRun(t *testing.T, n, recordBytes int, parentAlive, spoilSeed bool) (childCost uint64) {
	t.Helper()
	inner := seamFixture(t, n, recordBytes)

	parent := newWindowedLog[message.Message](inner, 0, 0, irDecodeInflation, irEntrySize)
	seed := parent.Read() // resident rows, the donation
	if spoilSeed {
		// A seed the seam probe must reject: same length, wrong content at the
		// last row, so newSeededLog falls back to decoding. Not a fabricated
		// failure -- it is the branch the constructor documents.
		spoiled := make([]Entry[message.Message], len(seed))
		copy(spoiled, seed)
		last := spoiled[len(spoiled)-1]
		last.FigaroLT = last.FigaroLT + 1_000_000
		spoiled[len(spoiled)-1] = last
		seed = spoiled
	}

	child := newSeededLog[message.Message](inner, 0, 0, irDecodeInflation, irEntrySize, seed)
	_ = child.Read() // force the child's own residency

	if !parentAlive {
		parent = nil
		seed = nil
	}

	withChild := heapAfterGC()
	runtime.KeepAlive(child)
	child = nil
	withoutChild := heapAfterGC()

	// The parent (and the inner log) must survive BOTH readings in the
	// parent-alive arm, or the delta includes bytes nobody was asking about.
	runtime.KeepAlive(parent)
	runtime.KeepAlive(inner)

	if withChild < withoutChild {
		return 0 // GC noise swamped the signal; reported as zero, never negative
	}
	return withChild - withoutChild
}

// TestForkSeamSharing is the measurement. It ASSERTS the falsifier registered
// at 44c82ab6 and prints the numbers either way: a measurement that only
// prints is not an instrument, and one that only asserts hides the number the
// decision needs.
func TestForkSeamSharing(t *testing.T) {
	const (
		records     = 1000
		recordBytes = 2326 // the store's measured mean record
	)
	encoded := uint64(records * recordBytes)

	marginal := seamRun(t, records, recordBytes, true, false)
	soleOwner := seamRun(t, records, recordBytes, false, false)

	t.Logf("FORK SEAM, %d records x %d B (encoded %d B = %.2f MiB)",
		records, recordBytes, encoded, float64(encoded)/(1<<20))
	t.Logf("  A  child marginal cost, PARENT ALIVE   = %10d B (%6.2f MiB)",
		marginal, float64(marginal)/(1<<20))
	t.Logf("  B  child cost, PARENT DROPPED          = %10d B (%6.2f MiB)",
		soleOwner, float64(soleOwner)/(1<<20))
	if soleOwner > marginal {
		t.Logf("  SHARED = B - A                         = %10d B (%6.2f MiB), A/B = %.3f",
			soleOwner-marginal, float64(soleOwner-marginal)/(1<<20),
			float64(marginal)/float64(soleOwner))
	}

	// THE REGISTERED FALSIFIER: A within 20% of B means the prefix is NOT
	// shared and the child paid for its own copy.
	if soleOwner == 0 {
		t.Fatalf("the sole-owner arm measured nothing (B=0): the instrument did not "+
			"reach the object, and no conclusion about sharing follows. A=%d", marginal)
	}
	ratio := float64(marginal) / float64(soleOwner)
	if ratio > 0.8 {
		t.Errorf("FALSIFIED: the fork's prefix is NOT shared with its parent. "+
			"A=%d B B=%d B ratio=%.3f (registered falsifier: A within 20%% of B). "+
			"newSeededLog's comment says a shallow copy shares every string; this says "+
			"the child paid for its own copy.", marginal, soleOwner, ratio)
	}
}

// TestForkSeamSharingCanary proves the instrument can report the failure it
// exists to exclude. It drives newSeededLog's documented fall-back to a decode
// and requires the marginal cost to RISE TOWARD the sole-owner cost.
//
// WITHOUT THIS, a small A is not evidence of sharing -- it is equally
// consistent with an instrument that reaches nothing at all, which is the
// shape 503b9650 named: an instrument that would report the same number if the
// sharing were gone is measuring a total again.
func TestForkSeamSharingCanary(t *testing.T) {
	const (
		records     = 1000
		recordBytes = 2326
	)
	shared := seamRun(t, records, recordBytes, true, false)
	spoiled := seamRun(t, records, recordBytes, true, true)

	t.Logf("CANARY: marginal cost with a seed the seam ACCEPTS = %d B", shared)
	t.Logf("CANARY: marginal cost with a seed the seam REJECTS = %d B", spoiled)
	if spoiled <= shared {
		t.Fatalf("THE INSTRUMENT IS NOT MEASURING SHARING. Forcing the decode path "+
			"did not raise the child's marginal cost (accepted=%d B, rejected=%d B). "+
			"A low number in the main test is therefore consistent with reaching "+
			"nothing at all, and no sharing conclusion may be drawn from it.",
			shared, spoiled)
	}
	t.Logf("CANARY PASSED: rejecting the seed cost an extra %d B (%.2f MiB), so the "+
		"instrument responds to the presence of sharing",
		spoiled-shared, float64(spoiled-shared)/(1<<20))
	_ = fmt.Sprint()
}
