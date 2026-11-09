package disk

import (
	"errors"
	"fmt"
	"github.com/jack-work/figaro/figwal/segment"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// forkSetup builds a log with the given codec containing entries
// 1..count, each payload "data{i}". SegmentSize is small enough that
// `entriesPerSeg` entries fit per segment, so rotation produces several
// sealed segments.
func forkSetup(t *testing.T, codec segment.SegmentCodec, count int, entriesPerSeg int) (string, *Log) {
	t.Helper()
	dir := t.TempDir()
	// Generous, so segment boundaries are predictable; tests do not depend
	// on the exact size.
	approxPerEntry := 80
	segSize := int64(approxPerEntry * entriesPerSeg)
	l, err := Open(dir, Options{SegmentSize: segSize, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= uint64(count); i++ {
		var payload []byte
		if codec == nil || codec.Name() == "binary" {
			payload = []byte(fmt.Sprintf("data%d", i))
		} else {
			payload = []byte(fmt.Sprintf(`{"i":%d}`, i))
		}
		if err := l.Write(i, payload); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	return dir, l
}

func TestForkChildRangeStopsAtUntruncatedParentBoundary(t *testing.T) {
	dir := t.TempDir()
	parent, err := Open(dir, Options{Codec: segment.BinaryCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := parent.Write(1, []byte("prefix")); err != nil {
		t.Fatal(err)
	}
	if err := parent.Write(2, []byte("legacy-future")); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(dir, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeForkMarker(childDir, 2); err != nil {
		t.Fatal(err)
	}
	child, err := Open(childDir, Options{Codec: segment.BinaryCodec{}, Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err := child.Write(2, []byte("owned-future")); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := child.Range(1, func(_ uint64, payload []byte) error {
		got = append(got, string(payload))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"prefix", "owned-future"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Range = %v, want %v", got, want)
	}
}

func TestForkRangeAcrossParent(t *testing.T) {
	// After a mid-segment fork, child.Range from index 1 should walk
	// the parent for the prefix and the child for the suffix.
	_, l := forkSetup(t, segment.BinaryCodec{}, 6, 3)
	defer l.Close()
	cut := uint64(3)
	child, err := forkChild(l, cut, "alt")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	for i := cut; i <= 7; i++ {
		if err := child.Write(i, []byte(fmt.Sprintf("c%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var seen []uint64
	err = child.Range(1, func(idx uint64, payload []byte) error {
		seen = append(seen, idx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{1, 2, 3, 4, 5, 6, 7}
	if len(seen) != len(want) {
		t.Fatalf("seen=%v want %v", seen, want)
	}
	for i, idx := range seen {
		if idx != want[i] {
			t.Fatalf("seen[%d]=%d want %d", i, idx, want[i])
		}
	}
}

func TestForkOfFork(t *testing.T) {
	// Depth >= 2: fork A from trunk, then fork B from A. Reads on B
	// should walk B -> A -> trunk.
	_, trunk := forkSetup(t, segment.BinaryCodec{}, 4, 2)
	defer trunk.Close()
	a, err := forkChild(trunk, 3, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Write(3, []byte("a3")); err != nil {
		t.Fatal(err)
	}
	if err := a.Write(4, []byte("a4")); err != nil {
		t.Fatal(err)
	}
	b, err := forkChild(a, 4, "b")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Write(4, []byte("b4")); err != nil {
		t.Fatal(err)
	}
	// B reads:
	//   1, 2: trunk (via a, via b)
	//   3: a
	//   4: b
	checks := map[uint64]string{
		1: "data1",
		2: "data2",
		3: "a3",
		4: "b4",
	}
	for idx, want := range checks {
		got, err := b.Read(idx)
		if err != nil {
			t.Fatalf("b.Read(%d): %v", idx, err)
		}
		if string(got) != want {
			t.Fatalf("b[%d]=%q want %q", idx, got, want)
		}
	}
}

func TestForkReadOnlyParentRejectsWrite(t *testing.T) {
	dir, l := forkSetup(t, segment.BinaryCodec{}, 3, 2)
	child, err := forkChild(l, 4, "alt")
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	l.Close()
	l, err = Open(dir, Options{Codec: segment.BinaryCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(4, []byte("should-fail")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
}

// N-ary: a branch point accepts more than one sibling at the same split
// point. (Old semantics rejected a second fork with ErrReadOnly.)
// Re-split below an existing branch point inserts an intermediate node
// and re-homes the existing children + suffix into the old-future. The
// original continuation must still read the full history through the new
// chain. (The user's a→b→c→{d,e,f}, then fork below c, case.)
func TestForkPendingBlocksOpen(t *testing.T) {
	dir, l := forkSetup(t, segment.BinaryCodec{}, 3, 2)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed fork by dropping the sentinel file.
	if err := os.WriteFile(filepath.Join(dir, ForkPendingName),
		[]byte("at=2\nchild=foo\nold=bar\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrForkPending) {
		t.Fatalf("want ErrForkPending, got %v", err)
	}
}

func TestStoreDedupsParent(t *testing.T) {
	_, l := forkSetup(t, segment.BinaryCodec{}, 4, 2)
	dir := l.dir
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen via store, fork twice, then reopen children via the same
	// store and assert they share the same parent pointer.
	s := NewStore()
	defer s.Close()
	trunk, err := s.Open(dir, Options{Codec: segment.BinaryCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := forkChild(trunk, 2, "a")
	if err != nil {
		t.Fatal(err)
	}
	// Close `a` so we can reopen it through the store and observe
	// dedup. Closing the child does NOT close the trunk segments.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	aReopened, err := s.Open(filepath.Join(dir, "a"), Options{Codec: segment.BinaryCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	if aReopened.parent != trunk {
		t.Fatalf("expected shared trunk pointer, got %p want %p",
			aReopened.parent, trunk)
	}
}

// A fork that drops its own front still opens: the marker says where the
// parent hands over, the front file says where the fork's own data now starts.
func TestTruncateFrontOnAForkReopens(t *testing.T) {
	_, root := forkSetup(t, segment.BinaryCodec{}, 3, 3)
	child, err := forkChild(root, root.LastIndex()+1, "child")
	if err != nil {
		t.Fatal(err)
	}
	childDir := child.Dir()
	opts := Options{SegmentSize: 80 * 3, Codec: segment.BinaryCodec{}}
	for i := uint64(4); i <= 30; i++ {
		if err := child.Write(i, []byte(fmt.Sprintf("data%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	cut := child.active.BaseIndex()
	if cut <= 4 {
		t.Fatal("expected the child to rotate")
	}
	if err := child.TruncateFront(cut); err != nil {
		t.Fatal(err)
	}
	child.Close()
	root.Close()

	reopened, err := Open(childDir, opts)
	if err != nil {
		t.Fatalf("reopen after front truncation: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Read(30)
	if err != nil || string(got) != "data30" {
		t.Fatalf("read 30 = %q, %v", got, err)
	}
	if _, err := reopened.Read(cut - 1); err == nil {
		t.Fatal("a dropped record was read, from the parent or anywhere")
	}
	if got, err := reopened.Read(2); err != nil || string(got) != "data2" {
		t.Fatalf("the parent's records still read through: %q, %v", got, err)
	}
}

func writeForkMarker(dir string, base uint64) error {
	return os.WriteFile(filepath.Join(dir, forkMarkerName), fmt.Appendf(nil, "base=%d\n", base), 0o644)
}

func forkChild(parent *Log, base uint64, name string) (*Log, error) {
	dir := filepath.Join(parent.dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeForkMarker(dir, base); err != nil {
		return nil, err
	}
	opts := parent.opts
	opts.Parent = parent
	parent.readOnly = true
	return Open(dir, opts)
}
