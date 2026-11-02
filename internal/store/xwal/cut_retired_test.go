package xwal

import (
	"fmt"
	"slices"
	"testing"
)

// A cut used to retire every hot store first, so no handle opened before the
// cut could write into the cut directory afterwards. Scoped, a store retired
// earlier can still hold the node's head; its final flush and close must not
// bring cut records back or write at a stale offset.
func TestARetiredStoreHoldingTheNodeCannotUndoACut(t *testing.T) {
	s, err := OpenStore(t.TempDir(), StoreOptions{Main: "main", SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Trunks.root
	n, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	fillNotes(t, s, n, 6)
	fillNotes(t, s, other, 1)

	held, err := s.Trunks.Head(n)
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := s.Trunks.Head(other)
	if err != nil {
		t.Fatal(err)
	}
	for i := 7; i <= 8; i++ {
		if _, err := s.Append(n, "main", 0, []byte(fmt.Sprintf(`{"m":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(n, "notes", uint64(i), []byte(fmt.Sprintf(`{"n":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Trunks.retireHot()
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.TruncateFrom(n, "notes", 4); err != nil {
		t.Fatal(err)
	}
	if err := keeper.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := noteLTs(t, s, n), []uint64{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("after the retired store closed: notes %v, want %v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir, StoreOptions{Main: "main", SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got, want := noteLTs(t, reopened, n), []uint64{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("on disk: notes %v, want %v", got, want)
	}
	if _, err := reopened.Append(n, "main", 0, []byte(`{"m":9}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Append(n, "notes", 9, []byte(`{"n":9}`), nil); err != nil {
		t.Fatal(err)
	}
	if got, want := noteLTs(t, reopened, n), []uint64{1, 2, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("the next append after the cut: notes %v, want %v", got, want)
	}
}
