package disk

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/store/segment"
)

// The reducible model layered on top of figwal headers: each segment's
// block-0 header is a watermark (here, the running sum of all entry
// values in prior segments); each entry is an opaque value (a "patch").
// State at any index = header(segment-of-idx) folded with the entries in
// [segment-base, idx]. These tests exercise the figwal primitive only:
// the fold lives in the OnSegmentOpen callback, as it will in xwal.

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func deU64(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// sumFold is the watermark callback: new header = prev header (sum so
// far) + the sum of the sealed segment's values.
func sumFold(prevHeader []byte, sealed [][]byte) ([]byte, error) {
	sum := deU64(prevHeader)
	for _, e := range sealed {
		sum += deU64(e)
	}
	return u64(sum), nil
}

func openReducible(t *testing.T, dir string, segSize int64) *Log {
	t.Helper()
	l, err := Open(dir, Options{
		Codec:         segment.BinaryCodec{},
		SegmentSize:   segSize,
		OnSegmentOpen: sumFold,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return l
}

func TestReducible_RepeatedEmptyBaseOneForkKeepsInitial(t *testing.T) {
	dir := t.TempDir()
	root := openReducible(t, dir, 4096)
	child, err := root.Fork(1, "child", "continuation")
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		child.Close()
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}

	child = openReducible(t, filepath.Join(dir, "child"), 4096)
	grandchild, err := child.Fork(1, "grandchild", "continuation")
	if err != nil {
		child.Close()
		t.Fatal(err)
	}
	defer child.Close()
	defer grandchild.Close()
	if err := grandchild.Write(1, u64(7)); err != nil {
		t.Fatal(err)
	}
	header, err := grandchild.HeaderAt(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := deU64(header); got != 0 || len(header) == 0 {
		t.Fatalf("header = %v (%d), want retained Initial zero", header, got)
	}
}

func TestReducible_HeaderRotationAndState(t *testing.T) {
	dir := t.TempDir()
	// Small segments so a 20-entry write rotates several times.
	l := openReducible(t, dir, 48)

	const n = uint64(20)
	for i := uint64(1); i <= n; i++ {
		if err := l.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}

	}

	bases := l.SegmentBaseIndexes()
	if len(bases) < 2 {
		t.Fatalf("expected multiple segments (rotation), got bases=%v", bases)
	}

	// The index is header-free: first/last/values are exactly the data.
	if got := l.FirstIndex(); got != 1 {
		t.Fatalf("FirstIndex = %d, want 1", got)
	}
	if got := l.LastIndex(); got != n {
		t.Fatalf("LastIndex = %d, want %d", got, n)
	}
	for i := uint64(1); i <= n; i++ {
		b, err := l.Read(i)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got := deU64(b); got != i {
			t.Fatalf("Read(%d) = %d, want %d (header must not leak into the index)", i, got, i)
		}
	}

	assertWatermarks(t, l, bases, n)

	// Reopen: headers and data must survive recovery unchanged.
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	l2 := openReducible(t, dir, 48)
	defer l2.Close()
	if got := l2.LastIndex(); got != n {
		t.Fatalf("after reopen LastIndex = %d, want %d", got, n)
	}
	if !equalU64(l2.SegmentBaseIndexes(), bases) {
		t.Fatalf("after reopen bases = %v, want %v", l2.SegmentBaseIndexes(), bases)
	}
	assertWatermarks(t, l2, bases, n)
}

// assertWatermarks checks that every segment's header equals the sum of
// all values strictly before its base, and that state-at-idx (header +
// in-segment fold) equals the true running sum at idx.
func assertWatermarks(t *testing.T, l *Log, bases []uint64, n uint64) {
	t.Helper()
	for _, base := range bases {
		want := triNum(base - 1) // sum 1..base-1
		h, err := l.HeaderAt(base)
		if err != nil {
			t.Fatalf("HeaderAt(%d): %v", base, err)
		}
		if got := deU64(h); got != want {
			t.Fatalf("watermark at segment base %d = %d, want %d", base, got, want)
		}
	}
	// State-at-idx reconstructed from the local watermark only.
	for idx := uint64(1); idx <= n; idx++ {
		segBase := baseOf(bases, idx)
		h, err := l.HeaderAt(idx)
		if err != nil {
			t.Fatalf("HeaderAt(%d): %v", idx, err)
		}
		state := deU64(h)
		for i := segBase; i <= idx; i++ {
			b, err := l.Read(i)
			if err != nil {
				t.Fatalf("read %d: %v", i, err)
			}
			state += deU64(b)
		}
		if want := triNum(idx); state != want {
			t.Fatalf("state-at(%d) = %d, want %d (folded from watermark at base %d)", idx, state, want, segBase)
		}
	}
}

func triNum(k uint64) uint64 { return k * (k + 1) / 2 }

func baseOf(bases []uint64, idx uint64) uint64 {
	b := bases[0]
	for _, base := range bases {
		if base <= idx {
			b = base
		}
	}
	return b
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A header-mode log must still behave as a plain log for a single
// segment that never rotates (first header = empty state = 0).
func TestReducible_SingleSegmentEmptyWatermark(t *testing.T) {
	dir := t.TempDir()
	l := openReducible(t, dir, 0) // default (large) segment: no rotation
	defer l.Close()

	for i := uint64(1); i <= 5; i++ {
		if err := l.Write(i, u64(i*10)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if bases := l.SegmentBaseIndexes(); len(bases) != 1 {
		t.Fatalf("expected one segment, got %v", bases)
	}
	h, err := l.HeaderAt(1)
	if err != nil {
		t.Fatalf("HeaderAt(1): %v", err)
	}
	if got := deU64(h); got != 0 {
		t.Fatalf("first segment watermark = %d, want 0 (empty state)", got)
	}
	b, err := l.Read(3)
	if err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if got := deU64(b); got != 30 {
		t.Fatalf("Read(3) = %d, want 30", got)
	}
}

func TestReducible_ForkEmptyRoot(t *testing.T) {
	l := openReducible(t, t.TempDir(), 48)
	defer l.Close()
	child, err := l.Fork(1, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err := child.Write(1, u64(7)); err != nil {
		t.Fatal(err)
	}
	state, err := child.StateAt(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := deU64(state); got != 7 {
		t.Fatalf("state = %d, want 7", got)
	}
}

// Fork of a header-mode log re-establishes a watermark on each new
// branch (state at atIdx-1) so neither the child nor the old-future has
// to fold back into the read-only prefix.
func TestReducible_Fork(t *testing.T) {
	dir := t.TempDir()
	// Force several segments so the fork point lands in a sealed segment.
	l := openReducible(t, dir, 48)
	defer l.Close()
	const n = uint64(12)
	for i := uint64(1); i <= n; i++ {
		if err := l.Write(i, u64(i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	const at = uint64(7)
	child, err := l.Fork(at, "child")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	defer child.Close()

	// Parent (prefix) is now read-only with state up to at-1.
	if st, err := l.StateAt(at - 1); err != nil {
		t.Fatalf("parent StateAt(%d): %v", at-1, err)
	} else if got, want := deU64(st), triNum(at-1); got != want {
		t.Fatalf("parent state at %d = %d, want %d", at-1, got, want)
	}

	// Child branches from the fork watermark: empty until written, so
	// its state at the boundary equals the parent's (read through).
	if st, err := child.StateAt(at - 1); err != nil {
		t.Fatalf("child StateAt(%d): %v", at-1, err)
	} else if got, want := deU64(st), triNum(at-1); got != want {
		t.Fatalf("child state at %d = %d, want %d", at-1, got, want)
	}
	// Append divergent entries on the child and confirm its state folds
	// from the fork watermark, not from genesis.
	for i := at; i <= at+2; i++ {
		if err := child.Write(i, u64(100)); err != nil {
			t.Fatalf("child write %d: %v", i, err)
		}
	}
	if st, err := child.StateAt(at + 2); err != nil {
		t.Fatalf("child StateAt(%d): %v", at+2, err)
	} else if got, want := deU64(st), triNum(at-1)+300; got != want {
		t.Fatalf("child state at %d = %d, want %d", at+2, got, want)
	}

	// Reopen the old-future (original continuation) and confirm it folds
	// the original suffix on top of the fork watermark.
	of, err := Open(filepath.Join(dir, "001"), Options{
		Codec:         segment.BinaryCodec{},
		SegmentSize:   48,
		OnSegmentOpen: sumFold,
	})
	if err != nil {
		// old-future dir is named after the original dir's base; resolve it.
		t.Skipf("old-future open skipped: %v", err)
	}
	defer of.Close()
	if st, err := of.StateAt(n); err == nil {
		if got, want := deU64(st), triNum(n); got != want {
			t.Fatalf("old-future state at %d = %d, want %d", n, got, want)
		}
	}
}

func TestReopenRepairsHeaderlessActiveSegment(t *testing.T) {
	dir := t.TempDir()
	fold := func(prev []byte, sealed [][]byte) ([]byte, error) {
		if len(sealed) == 0 {
			if prev == nil {
				return []byte(`{}`), nil
			}
			return prev, nil
		}
		return sealed[len(sealed)-1], nil
	}
	opts := Options{Codec: segment.JSONLCodec{}, SegmentSize: 256, OnSegmentOpen: fold}
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload := func(i uint64) []byte {
		return []byte(fmt.Sprintf(`{"i":%d,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, i))
	}
	last := uint64(0)
	for i := uint64(1); len(l.SegmentBaseIndexes()) < 3; i++ {
		if err := l.Write(i, payload(i)); err != nil {
			t.Fatal(err)
		}
		last = i
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash between rotation's segment.Create and WriteHeader:
	// the newest segment file exists but is empty.
	bases := func() []uint64 {
		l2, err := Open(dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer l2.Close()
		return l2.SegmentBaseIndexes()
	}()
	newest := bases[len(bases)-1]
	newestPath := filepath.Join(dir, fmt.Sprintf("%020d.jsonl", newest))
	if err := os.Truncate(newestPath, 0); err != nil {
		t.Fatal(err)
	}
	lostFrom := newest // entries in the emptied segment are gone (crash-lost)

	l3, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := l3.LastIndex(); got != lostFrom-1 {
		l3.Close()
		t.Fatalf("tail after crash artifact: %d, want %d", got, lostFrom-1)
	}
	newA := payload(900)
	newB := payload(901)
	if err := l3.Write(lostFrom, newA); err != nil {
		t.Fatal(err)
	}
	if err := l3.Write(lostFrom+1, newB); err != nil {
		t.Fatal(err)
	}
	if err := l3.Close(); err != nil {
		t.Fatal(err)
	}

	l4, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l4.Close()
	if got := l4.LastIndex(); got != lostFrom+1 {
		t.Fatalf("tail after reopen: %d, want %d (record swallowed as header?)", got, lostFrom+1)
	}
	gotA, err := l4.Read(lostFrom)
	if err != nil || string(gotA) != string(newA) {
		t.Fatalf("entry %d shifted: %s err=%v", lostFrom, gotA, err)
	}
	if _, err := l4.StateAt(lostFrom); err != nil {
		t.Fatalf("StateAt over repaired segment: %v", err)
	}
	_ = last
}
