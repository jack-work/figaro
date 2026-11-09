package log

import (
	"bytes"
	"testing"
)

// DiscardFrom is how a refused write takes its record back: pending or
// already on disk, the log must end where the last acknowledged record did.
func TestDiscardFromTakesBackPendingAndDurableRecords(t *testing.T) {
	l, err := Open(t.TempDir(), Options{SegmentSize: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := uint64(1); i <= 2; i++ {
		if err := l.Write(i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	for i := uint64(3); i <= 4; i++ {
		if err := l.Write(i, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}

	if err := l.DiscardFrom(3); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := l.PendingBounds(); ok {
		t.Fatal("discarded records are still pending")
	}
	if last := l.LastIndex(); last != 2 {
		t.Fatalf("last index %d after discarding pending 3..4, want 2", last)
	}

	if err := l.DiscardFrom(2); err != nil {
		t.Fatal(err)
	}
	if last := l.LastIndex(); last != 1 {
		t.Fatalf("last index %d after discarding durable 2, want 1", last)
	}
	if err := l.Write(2, []byte("again")); err != nil {
		t.Fatalf("the next write must take the discarded index: %v", err)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := l.Read(2)
	if err != nil || !bytes.Equal(got, []byte("again")) {
		t.Fatalf("read 2 = %q, %v", got, err)
	}
}
