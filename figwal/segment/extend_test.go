package segment

import (
	"bytes"
	"path/filepath"
	"runtime"
	"testing"
)

// The writer reads its newest record before each append, as the IR door does.
// Each record must be resident and charged once: extending the tail by
// re-putting the chunk prefix left every prefix resident and charged the
// budget about sixteen times the payload. Evicting the node must then hand
// every charged byte back.
func TestExtendTailChargesEachRecordOnce(t *testing.T) {
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(32 << 20)
	payloadBudget.TrimIdle(-1)

	s, err := Create(filepath.Join(t.TempDir(), "seg"), BinaryCodec{}, 1, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := []byte(`{"p":"` + string(bytes.Repeat([]byte("x"), 100)) + `"}`)
	const records = 256
	for i := 0; i < records; i++ {
		if i > 0 {
			if _, err := s.ReadIndex(uint64(i - 1)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReadIndex(records - 1); err != nil {
		t.Fatal(err)
	}

	unit := int64(len(payload) + sliceHeaderBytes)
	if got, want := CachedBytes(), records*unit; got != want {
		t.Fatalf("%d records of %d B charged %d B, want %d (x%.1f)", records, unit, got, want, float64(got)/float64(want))
	}
	var covered uint64
	for _, r := range payloadCache.Index(s.path) {
		if !r.Resident {
			continue
		}
		if r.From != covered {
			t.Fatalf("resident runs overlap or leave a hole: run (%d,%d] after %d", r.From, r.To, covered)
		}
		covered = r.To
	}
	if covered != records {
		t.Fatalf("resident runs cover (0,%d], want (0,%d]", covered, records)
	}

	payloadBudget.TrimIdle(-1)
	if got := CachedBytes(); got != 0 {
		t.Fatalf("after evicting the node, %d bytes still charged", got)
	}
	if got := CachedRanges(); got != 0 {
		t.Fatalf("after evicting the node, %d ranges still held", got)
	}
}

// A segment whose payload cache was dropped holds one hollow run per appended
// record. Reading it back must refill a chunk at a time: refilled one record
// at a time, each refill copies the node's whole run index, and 8000 records
// cost about 500 MiB.
func TestReadingBackADroppedTailRefillsByChunk(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's allocations are not the read's")
	}
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(1 << 30)
	payloadBudget.TrimIdle(-1)

	s, err := Create(filepath.Join(t.TempDir(), "seg"), BinaryCodec{}, 1, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := []byte(`{"p":"record"}`)
	const records = 8000
	for i := 0; i < records; i++ {
		if i > 0 {
			if _, err := s.ReadIndex(uint64(i - 1)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if runs := len(payloadCache.Index(s.path)); runs < records/2 {
		t.Fatalf("the tail left %d runs for %d records; the case under test is one run per record", runs, records)
	}
	s.handle().Drop()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := uint64(0); i < records; i++ {
		if _, err := s.ReadIndex(i); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 64<<20 {
		t.Fatalf("reading back %d records allocated %.1f MiB, ceiling 64 MiB", records, float64(got)/(1<<20))
	}
}
