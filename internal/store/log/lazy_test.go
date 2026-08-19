package log

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

// Opening a log used to copy every payload of every channel into memory. It
// was figwal's largest resident structure and nothing bounded it: a store's
// whole history became its resident set the moment anything touched it.
// These are the properties that replaced it.

func lazyFixture(t *testing.T, n int, size int) (string, Options) {
	t.Helper()
	dir := t.TempDir()
	opts := Options{SegmentSize: 1 << 16}
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, size)
	for i := 1; i <= n; i++ {
		payload[0] = byte(i)
		if err := l.Write(uint64(i), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, opts
}

func TestOpenRetainsNoPayloads(t *testing.T) {
	dir, opts := lazyFixture(t, 400, 512)
	before := segment.CachedBytes()
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := segment.CachedBytes() - before; got != 0 {
		t.Fatalf("open retained %d payload bytes, want 0", got)
	}
	if l.LastIndex() != 400 {
		t.Fatalf("LastIndex = %d, want 400", l.LastIndex())
	}
	// A read is what makes a payload resident, and only for the segment it
	// lands in -- not for the log.
	if _, err := l.Read(1); err != nil {
		t.Fatal(err)
	}
	after := segment.CachedBytes() - before
	if after == 0 {
		t.Fatal("a read cached nothing")
	}
	if after >= 400*512 {
		t.Fatalf("one read made %d bytes resident: that is the whole log again", after)
	}
}

func TestEvictedPayloadsStillRead(t *testing.T) {
	dir, opts := lazyFixture(t, 400, 512)
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	want := make(map[uint64]byte, 400)
	if err := l.Range(1, func(idx uint64, payload []byte) error {
		want[idx] = payload[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if segment.CachedBytes() == 0 {
		t.Fatal("a full range cached nothing")
	}

	// Squeeze the budget to nothing: every block is dropped, and every
	// record is still there, because the file always had it.
	old := segment.CacheBudget()
	segment.SetCacheBudget(0)
	defer segment.SetCacheBudget(old)
	if got := segment.CachedBytes(); got != 0 {
		t.Fatalf("after a zero budget, %d bytes still held", got)
	}
	for idx, b := range want {
		got, err := l.Read(idx)
		if err != nil {
			t.Fatalf("read %d after eviction: %v", idx, err)
		}
		if got[0] != b {
			t.Fatalf("read %d after eviction = %d, want %d", idx, got[0], b)
		}
	}
}

func TestCacheBudgetIsRespected(t *testing.T) {
	old := segment.CacheBudget()
	defer segment.SetCacheBudget(old)
	segment.SetCacheBudget(0)
	segment.SetCacheBudget(96 << 10)

	dir, opts := lazyFixture(t, 2000, 512)
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	seen := 0
	if err := l.Range(1, func(uint64, []byte) error { seen++; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 2000 {
		t.Fatalf("ranged %d records, want 2000", seen)
	}
	// The whole log is a megabyte; the budget is 96 KiB. Eviction runs when
	// a load crosses it, so the held total may briefly include the block
	// that crossed -- but it cannot be the whole log.
	if got := segment.CachedBytes(); got > 4*(96<<10) {
		t.Fatalf("held %d bytes against a 96 KiB budget", got)
	}
}

// An unsynced record has no segment to be read from, so the pending buffer
// must serve it -- and must not serve it twice once it lands.
func TestPendingRecordsReadAndDoNotDouble(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{Codec: segment.JSONLCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := uint64(1); i <= 5; i++ {
		if err := l.write(i, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, ok := l.PendingBounds(); !ok {
		t.Fatal("nothing pending after five unsynced writes")
	}
	got, err := l.Read(3)
	if err != nil || string(got) != `{"i":3}` {
		t.Fatalf("unsynced read = %q, %v", got, err)
	}
	count := func() int {
		n := 0
		if err := l.Range(1, func(uint64, []byte) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 5 {
		t.Fatalf("ranged %d unsynced records, want 5", n)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 5 {
		t.Fatalf("ranged %d records after the sync, want 5", n)
	}
	var desc []uint64
	if err := l.ScanFromEnd(0, func(idx uint64, _ []byte) error {
		desc = append(desc, idx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(desc) != 5 || desc[0] != 5 || desc[4] != 1 {
		t.Fatalf("ScanFromEnd = %v, want 5..1", desc)
	}
}
