package xwal

import (
	"errors"
	"testing"
)

func fencedStore(t *testing.T) (*Store, string, TrunkID) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"n":1}`), nil); err != nil {
		t.Fatal(err)
	}
	return s, dir, tr
}

// A closed store hands out no handle and takes no topology change, so
// nothing can write behind a cleared .unclean.
func TestClosedStoreRefusesHeadsAndTopology(t *testing.T) {
	s, dir, tr := fencedStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if pathExists(uncleanPath(dir)) {
		t.Fatal(".unclean survived a clean close")
	}
	if _, err := s.Head(string(tr)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Head after Close: got %v, want ErrClosed", err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"n":2}`), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after Close: got %v, want ErrClosed", err)
	}
	if _, err := s.SpawnUnderRoot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("SpawnUnderRoot after Close: got %v, want ErrClosed", err)
	}
	if _, err := s.ForkTail(string(tr)); !errors.Is(err, ErrClosed) {
		t.Fatalf("ForkTail after Close: got %v, want ErrClosed", err)
	}
	if pathExists(uncleanPath(dir)) {
		t.Fatal("a refused call re-marked the store")
	}
}

// Abandon is the close for a stop that could not account for every writer:
// the fence goes up and .unclean stays, so the next open repairs.
func TestAbandonedStoreKeepsUncleanAndRefuses(t *testing.T) {
	s, dir, tr := fencedStore(t)
	if err := s.Abandon(); err != nil {
		t.Fatal(err)
	}
	if !pathExists(uncleanPath(dir)) {
		t.Fatal(".unclean was cleared by Abandon")
	}
	if _, err := s.Head(string(tr)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Head after Abandon: got %v, want ErrClosed", err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("reopen after Abandon: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

// A head still borrowed at Close keeps .unclean, and the fence still goes
// up: the borrower gets no second handle.
func TestCloseWithABorrowedHeadKeepsUncleanAndFences(t *testing.T) {
	s, dir, tr := fencedStore(t)
	x, err := s.Head(string(tr))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("Close succeeded with a head borrowed")
	}
	if !pathExists(uncleanPath(dir)) {
		t.Fatal(".unclean was cleared with a head borrowed")
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(string(tr)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Head after a refused Close: got %v, want ErrClosed", err)
	}
}
