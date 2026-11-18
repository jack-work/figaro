package disk

import (
	"fmt"
	"testing"
)

// A deferred fsync can find the segment it captured sealed and then closed
// by two rotations. Rotation fsynced it under mu, so the closed file is
// durable: reporting it as a failed sync would make a writer discard records
// that are on disk.
func TestASyncWhoseSegmentRotatedClosedReportsNoFailure(t *testing.T) {
	l, err := Open(t.TempDir(), Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(1, []byte("rec-01")); err != nil {
		t.Fatal(err)
	}
	next := uint64(2)
	l.beforeSync = func() {
		l.beforeSync = nil
		sealedBefore := len(l.sealed)
		for len(l.sealed) < sealedBefore+2 {
			if err := l.Write(next, []byte(fmt.Sprintf("rec-%02d", next))); err != nil {
				t.Fatal(err)
			}
			next++
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatalf("a sync over a segment rotation already made durable failed: %v", err)
	}
}
