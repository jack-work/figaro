package segment

import (
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A MISS ON ONE RECORD MUST NOT READ THE WHOLE SEGMENT.
//
// This is the hazard test for making the cache unit A RANGE OF RECORDS rather
// than a whole file. It is written BEFORE the change and it is expected to be
// RED: today `cachedPayloads` misses into `readAllPayloads`, which is a plain
// loop calling `codec.ReadFrame(s.f, off, nextOff)` ONCE PER RECORD over the
// entire segment, however few records the caller asked for.
//
// WHY A COUNT AND NOT A TIME. The question is "how many times", which is what
// this campaign counts wherever it can: a count is load-immune, and Gluck is on
// the box. A duration would answer the same question worse and would decay with
// the machine.
//
// WHY THE CODEC AND NOT readAllPayloads. Counting inside readAllPayloads would
// mean editing the function under test to observe it. The codec is INJECTED at
// Open/Create (SegmentCodec is a parameter, not a global), so a decorator sits
// exactly where the reads pass and PERTURBS NOTHING. c64cacf2 pointed this out
// and it is the difference between an instrument and a modification.
//
// THE APPROVED COMPLEXITY CHANGE THIS PINS (plans/segment-cache-unit.md,
// Gluck: "O(log k) is fine, totally expected"):
//
//	ReadIndex, MISS  O(records in segment) -> O(records in range)
//	ReadIndex, HIT   O(1) lock-free        -> O(log K) LOCK-FREE
//
// NOT APPROVED, and this file must never be made to pass by it: routing the hit
// through tree.Range under the package-wide mutex. docs/store/tree.md forbids
// an Evicted hook that takes a lock -- it deadlocks under budget pressure with
// concurrent readers -- so the fast structure must be an immutable value
// republished by pointer swap, which is also what keeps the hit lock-free.

// countingCodec is a pass-through that tallies ReadFrame calls and the bytes
// those calls span. It decorates; it does not reimplement.
type countingCodec struct {
	SegmentCodec
	frames atomic.Int64
	bytes  atomic.Int64
}

func (c *countingCodec) ReadFrame(r io.ReaderAt, off, nextOff int64) ([]byte, int, error) {
	c.frames.Add(1)
	if nextOff > off {
		c.bytes.Add(nextOff - off)
	}
	p, n, err := c.SegmentCodec.ReadFrame(r, off, nextOff)
	return p, n, err
}

// segmentOfRecords writes n records and returns the path, closed.
func segmentOfRecords(t *testing.T, n int, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seg")
	s, err := Create(path, BinaryCodec{}, 1, 1<<26)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	for i := 0; i < n; i++ {
		if _, err := s.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAMissOnOneRecordDoesNotReadTheWholeSegment(t *testing.T) {
	const records = 200
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(8 << 20) // room to cache; the question is HOW MUCH is read

	path := segmentOfRecords(t, records, 512)

	cc := &countingCodec{SegmentCodec: BinaryCodec{}}
	s, err := OpenReadOnly(path, cc, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// VACUITY GUARD: the fixture must really hold `records` records, or a low
	// count below means an empty segment rather than a bounded read.
	if got := s.Count(); got != records {
		t.Fatalf("fixture holds %d records, want %d", got, records)
	}

	// ONE RECORD, COLD. Everything before this line is setup; the count that
	// matters is the one this single call produces.
	before := cc.frames.Load()
	if _, err := s.ReadIndex(0); err != nil {
		t.Fatal(err)
	}
	frames := cc.frames.Load() - before

	t.Logf("one cold ReadIndex over a %d-record segment: %d ReadFrame calls, %d bytes",
		records, frames, cc.bytes.Load())

	// THE ASSERTION. A miss on one record may read a BOUNDED range around it,
	// never the segment. The bound is deliberately generous -- this is not a
	// claim about the range size, which is a design choice, but about the
	// difference between O(range) and O(segment).
	if frames >= records {
		t.Fatalf("a miss on ONE record made %d ReadFrame calls over a %d-record "+
			"segment: the whole file was materialised to answer for one entry. "+
			"That is readAllPayloads, and it is what the range unit exists to "+
			"replace", frames, records)
	}
	if frames > records/4 {
		t.Errorf("a miss on one record made %d ReadFrame calls; bounded, but over a "+
			"quarter of the segment (%d records). The range asked for should not "+
			"scale with the file", frames, records)
	}
}

// AND THE OTHER HALF OF THE SAME PROPERTY: reading the WHOLE segment record by
// record must not be worse than reading it once. A range unit that made each
// read materialise its own overlapping range would fix the single-record case
// and ruin the sequential scan, which is what the fig IR encode path does.
//
// This is here so that the fix for the test above cannot be a regression for
// the traversal that matters most.
func TestASequentialScanDoesNotRereadRecords(t *testing.T) {
	const records = 200
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(8 << 20)

	path := segmentOfRecords(t, records, 512)

	cc := &countingCodec{SegmentCodec: BinaryCodec{}}
	s, err := OpenReadOnly(path, cc, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := uint64(0); i < records; i++ {
		if _, err := s.ReadIndex(i); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	frames := cc.frames.Load()
	t.Logf("a full sequential scan of %d records: %d ReadFrame calls", records, frames)

	// Each record materialised AT MOST ONCE, with slack for range boundaries.
	if frames > records*2 {
		t.Fatalf("a sequential scan of %d records made %d ReadFrame calls: records are "+
			"being re-read, so the range unit is thrashing rather than bounding",
			records, frames)
	}
}

// AN APPEND EXTENDS WHAT IS RESIDENT AND NEVER FAULTS IT IN.
//
// THE PROPERTY, NOT THE SYMPTOM. An append that materializes its own tail
// turns the cache into a FIGHT WITH ITS OWN EVICTOR: the sweep drops a run,
// the next append re-creates it, and an append loop racing a sweep livelocks
// with each undoing the other. It cost a 25-second hang in
// TestCacheAccountingSurvivesAppendVersusEvict to see, and the symptom there
// was a timeout, which names nothing.
func TestAnAppendNeverFaultsInItsOwnTail(t *testing.T) {
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(8 << 20)

	path := segmentOfRecords(t, 64, 128)
	cc := &countingCodec{SegmentCodec: BinaryCodec{}}
	s, err := Open(path, cc, 1, 1<<26)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Nothing resident: the cache has never been read through.
	s.DropCache()
	before := cc.frames.Load()
	if _, err := s.Append(make([]byte, 128)); err != nil {
		t.Fatal(err)
	}
	if got := cc.frames.Load() - before; got != 0 {
		t.Fatalf("an append READ %d records from the file. It must EXTEND what is "+
			"resident and never FAULT IT IN -- an append that materializes re-creates "+
			"residency the evictor just dropped, and the two then livelock", got)
	}
}
