package disk

import (
	"bytes"
	"testing"

	"github.com/jack-work/figaro/figwal/segment"
)

// Issue #59: every rotation used to leave the sealed segment open for the life
// of the log, with its descriptor, its offset index and its watermark header,
// so a long-lived form grew by one full folded state per rotation. A rotation
// now closes all but the newest sealed segment, and a read into an older one
// reopens it.
func TestRotationsDoNotAccumulateOpenSealedSegments(t *testing.T) {
	open0, headers0 := segment.OpenSegments()
	l := openReducible(t, t.TempDir(), 256)
	defer l.Close()

	payload := bytes.Repeat([]byte{7}, 64)
	const n = 200
	for i := uint64(1); i <= n; i++ {
		if err := l.Write(i, append(u64(i), payload...)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if len(l.sealed) < 20 {
		t.Fatalf("only %d rotations; shrink the segment", len(l.sealed))
	}
	open1, headers1 := segment.OpenSegments()
	if got := open1 - open0; got > 3 {
		t.Fatalf("%d segments open after %d rotations, want the active one and the newest sealed",
			got, len(l.sealed))
	}
	if headers1-headers0 > 64 {
		t.Fatalf("headers resident grew by %d bytes", headers1-headers0)
	}

	for _, idx := range []uint64{1, 2, n / 2, n} {
		got, err := l.Read(idx)
		if err != nil {
			t.Fatalf("read %d from a closed sealed segment: %v", idx, err)
		}
		if deU64(got[:8]) != idx {
			t.Fatalf("read %d: got %d", idx, deU64(got[:8]))
		}
	}
	if _, err := l.StateAt(n / 2); err != nil {
		t.Fatalf("state at %d after its segment closed: %v", n/2, err)
	}
}
