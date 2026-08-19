package disk

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

// Opening a log must not open its history. These are the properties that
// replaced "open everything, scan everything".

func manySegments(t *testing.T, n int) (string, Options) {
	t.Helper()
	dir := t.TempDir()
	opts := Options{SegmentSize: 4 << 10, Codec: segment.BinaryCodec{}}
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 256)
	for i := 1; i <= n; i++ {
		payload[0] = byte(i)
		payload[1] = byte(i >> 8)
		if err := l.Write(uint64(i), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, opts
}

func openCount(l *Log) int {
	n := 0
	for _, ss := range l.sealed {
		if ss.loaded() != nil {
			n++
		}
	}
	return n
}

func TestOpenDoesNotOpenSealedSegments(t *testing.T) {
	dir, opts := manySegments(t, 600)
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if len(l.sealed) < 3 {
		t.Fatalf("fixture produced %d sealed segments, want several", len(l.sealed))
	}
	if got := openCount(l); got != 0 {
		t.Fatalf("open opened %d sealed segments, want 0", got)
	}
	// The bounds are answerable from the names alone.
	if got := l.FirstIndex(); got != 1 {
		t.Fatalf("FirstIndex = %d, want 1", got)
	}
	if got := l.LastIndex(); got != 600 {
		t.Fatalf("LastIndex = %d, want 600", got)
	}
	if got := openCount(l); got != 0 {
		t.Fatalf("answering the bounds opened %d segments", got)
	}

	// A read opens exactly the segment it lands in.
	if _, err := l.Read(1); err != nil {
		t.Fatal(err)
	}
	if got := openCount(l); got != 1 {
		t.Fatalf("one read opened %d segments, want 1", got)
	}
	if _, err := l.Read(2); err != nil {
		t.Fatal(err)
	}
	if got := openCount(l); got != 1 {
		t.Fatalf("a second read in the same segment opened %d, want 1", got)
	}
}

func TestLazySegmentsServeEveryRecord(t *testing.T) {
	dir, opts := manySegments(t, 600)
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 1; i <= 600; i++ {
		payload, err := l.Read(uint64(i))
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if payload[0] != byte(i) || payload[1] != byte(i>>8) {
			t.Fatalf("read %d returned the wrong record", i)
		}
	}
	// Descending, which routes through the same lookup.
	seen := 0
	if err := l.ScanFromEnd(600, func(idx uint64, payload []byte) error {
		if payload[0] != byte(idx) || payload[1] != byte(idx>>8) {
			return fmt.Errorf("scan %d returned the wrong record", idx)
		}
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 600 {
		t.Fatalf("scanned %d records, want 600", seen)
	}
}

// TruncateFront unlinks whole sealed segments. It must not have to OPEN a
// file in order to delete it, and what survives must still read correctly.
func TestTruncateFrontWithoutOpening(t *testing.T) {
	dir, opts := manySegments(t, 600)
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cut := l.sealed[1].BaseIndex()
	if err := l.TruncateFront(cut); err != nil {
		t.Fatal(err)
	}
	if got := openCount(l); got != 0 {
		t.Fatalf("truncation opened %d segments", got)
	}
	if got := l.FirstIndex(); got != cut {
		t.Fatalf("FirstIndex after truncation = %d, want %d", got, cut)
	}
	payload, err := l.Read(cut)
	if err != nil {
		t.Fatalf("read at the new front: %v", err)
	}
	if payload[0] != byte(cut) || payload[1] != byte(cut>>8) {
		t.Fatal("the new front returned the wrong record")
	}
	if _, err := l.Read(cut - 1); err == nil {
		t.Fatal("a truncated record was still readable")
	}
}
