package segment

import (
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A miss on one record must not read the whole segment. Counted at the
// injected codec rather than timed: a count is load-immune, and the
// decorator sits where the reads pass without modifying the function under
// test. The hit must stay lock-free: an immutable value republished by
// pointer swap, never tree.Range under the package-wide mutex.

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

// The other half of the same property: reading the whole segment record by
// record must not be worse than reading it once, or the fix for the test
// above ruins the sequential scan the IR encode path runs.
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

// An append extends what is resident and never faults it in: an append
// that materializes its own tail turns the cache into a fight with its own
// evictor, and an append loop racing a sweep livelocks.
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
