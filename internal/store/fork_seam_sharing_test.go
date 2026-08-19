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
// FINISHED 2026-08-19 (dec6ef8a). THE RESULT:
//
//	A  child marginal cost, PARENT ALIVE   =   204,688 B
//	B  child cost, PARENT DROPPED          = 3,056,928 B
//	SHARED = B - A                         = 2,852,240 B   A/B = 0.067
//	CANARY: seed ACCEPTED 204,912 B; seed REJECTED 3,060,672 B
//
// A FORK'S RESIDENT PREFIX IS 93.3% SHARED WITH ITS PARENT. The child pays 6.7%
// of what the same prefix costs it when it is the sole owner -- 205 B per record
// of Entry struct against a 2,326 B record whose payload it does not copy.
//
// IT TOOK TWO CORRECTIONS, AND BOTH WERE THE INSTRUMENT RATHER THAN THE CODE.
//
// FIRST, THE HARNESS HELD THE STRINGS. Both arms read one MemLog that the
// fixture kept alive, and a MemLog retains every decoded record forever, so
// dropping the parent freed nothing and B measured the same object A did --
// 204,912 B twice, TO THE BYTE. Two numbers agreeing to the byte are a suspect,
// not a finding. The fix is the disk-backed fixture above.
//
// SECOND, AND ONLY VISIBLE AFTER THE FIRST WAS FIXED: `parent = nil` DID NOT
// DROP THE PARENT. The pointer survived in a stack slot the collector still
// scans, so the child never became the sole owner and B stayed at 204,896 --
// still within a hair of A, still looking like a finding. What exposed it was
// running the same sequence in a DIFFERENT FRAME SHAPE, where the identical arm
// produced 3,061,184 B. The parent is now built inside a function that RETURNS,
// and the parent-alive arm holds it in a package-level variable instead of a
// local.
//
//	A DEAD POINTER IN A LIVE STACK SLOT IS INDISTINGUISHABLE, TO THIS
//	INSTRUMENT, FROM AN OBJECT SOMEBODY MEANT TO KEEP.
//
// AND THE CANARY WAS RIGHT BOTH TIMES, WHICH IS WHY IT IS FIRST-CLASS HERE.
// Under the original MemLog fixture it fired in the WRONG DIRECTION -- spoiling
// the seed made the marginal cost FALL three orders of magnitude -- because
// newWindowedLog PRE-READS NOTHING when window and budget are both zero. So
// newSeededLog's documented degradation is not "decode a copy now", it is
// "RETAIN NOTHING UNTIL SOMEONE READS", a property of the constructor that
// nothing else in the tree asserts. With a disk-backed fixture the canary
// reports what it was built to report.
//
// WHAT THIS MEANS FOR THE CONSOLIDATION, stated so the number is not spent on
// the wrong question: TODAY'S ONE-SHOT DONATION ALREADY SHARES 93% OF A FORK'S
// PREFIX. A tree-shaped cache whose prefix residency is structural therefore
// buys CORRECTNESS AND STRUCTURE -- a lineage a copy can name, no probe
// carrying the guarantee, no second seeding path -- and NOT memory. That is a
// different justification and it must be argued as that one.
//
// LIMITS. Raw byte payloads: typed SDK structs are unmeasured and will share
// less. One lineage, one depth. And the segment payload cache is DISABLED for
// the duration rather than tolerated as a constant.
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

// seamFixture builds n records of recordBytes each in a REAL DISK-BACKED aria
// and returns a log over it. Scale is taken from the owner's real store rather
// than from taste: the census this aria ran measured translation channels at
// p99 2,463,337 B with a mean record of 2,326 B, so 1000 x 2326 is the p99
// shape.
//
// DISK-BACKED IS THE WHOLE CORRECTION (dec6ef8a, 2026-08-19, on the note this
// file was committed red with). The first version read both arms from one
// MemLog THAT THE HARNESS KEPT ALIVE, and a MemLog retains every decoded record
// forever: the payload strings were held by the fixture in both arms, so
// dropping the parent freed nothing and arm B measured the same object arm A
// did -- 204,912 B twice, to the byte. The precondition the experiment rests on
// (that dropping the parent leaves the child the sole owner of those strings)
// was violated by the instrument.
//
// AND THE SEGMENT PAYLOAD CACHE IS DISABLED FOR THE DURATION. It sits BELOW
// both arms holding raw frames under a process-global budget, which the
// original note called out as a constant this method cannot separate. It can be
// removed rather than tolerated: a zero budget drops what is held and makes
// every read a pread, so what remains resident is the two caches under test and
// nothing else.
func seamFixture(t *testing.T, n, recordBytes int) Log[message.Message] {
	t.Helper()

	prev := SegmentCacheBudget()
	SetSegmentCacheBudget(0)
	t.Cleanup(func() { SetSegmentCacheBudget(prev) })

	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}

	outfit, err := be.CreateOutfit("seam", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, recordBytes)
	for j := range blob {
		blob[j] = 'x'
	}
	for i := 0; i < n; i++ {
		if _, err := log.Append(Entry[message.Message]{
			Payload: message.Message{
				Role:    message.RoleInput,
				Content: []message.Content{message.TextContent(fmt.Sprintf("%d%s", i, blob))},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// THE WRITING BACKEND IS CLOSED AND THE READER OPENS THE STORE AFRESH.
	// XwalBackend memoises ONE cachedLog per aria, and that memo retains every
	// decoded row for the life of the backend -- a THIRD holder of the very
	// strings this experiment is trying to attribute to two. Evicting it is not
	// enough to trust: closing the backend and opening a new store leaves
	// nothing behind that could hold them.
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return newXwalLog[message.Message](st, id, chanIR, true)
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

	// THE PARENT IS DROPPED BY CONSTRUCTION, NOT BY ASSIGNMENT (dec6ef8a,
	// 2026-08-19). `parent = nil` in this frame is not enough: the pointer
	// survives in a stack slot the collector still scans, so the parent's rows
	// stay reachable and the child never becomes the sole owner. That is
	// exactly how this instrument reported A == B to the byte -- twice, once
	// with a MemLog holding the strings and once with a stack slot holding the
	// parent -- and a diagnostic in a DIFFERENT frame shape produced 3,061,184
	// B for the same arm, which is the tell.
	//
	// Building the parent inside a function that returns only the seed puts it
	// in a frame that has RETURNED. There is no slot left to scan.
	makeSeed := func() []Entry[message.Message] {
		parent := newWindowedLog[message.Message](inner, 0, 0, irDecodeNum, irDecodeDenom, irEntrySize)
		seed := parent.Read() // a COPY of the rows; the strings are shared
		if parentAlive {
			keptParent = parent // package-level, so the parent outlives this frame
		}
		runtime.KeepAlive(parent)
		return seed
	}
	seed := makeSeed()

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

	child := newSeededLog[message.Message](inner, 0, 0, irDecodeNum, irDecodeDenom, irEntrySize, seed)
	_ = child.Read() // force the child's own residency
	seed = nil

	withChild := heapAfterGC()
	runtime.KeepAlive(child)
	child = nil
	withoutChild := heapAfterGC()

	// The inner log must survive both readings, or the delta includes bytes
	// nobody was asking about.
	runtime.KeepAlive(inner)
	keptParent = nil

	if withChild < withoutChild {
		return 0 // GC noise swamped the signal; reported as zero, never negative
	}
	return withChild - withoutChild
}

// keptParent holds the parent for the PARENT-ALIVE arm from outside every
// measuring frame. A package-level variable is the bluntest possible way to say
// "this is alive on purpose", and bluntness is the point: the arm that needs
// the parent alive must not depend on a stack slot, since the arm that needs it
// DEAD was ruined by one.
var keptParent *cachedLog[message.Message]

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
