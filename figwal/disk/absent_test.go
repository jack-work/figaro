package disk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A node that never wrote a channel has no directory there. Opening it for
// reading must not create one: an empty log on disk owns its numbering
// from 1 and would read as a write.
func TestAnAbsentDirOpensEmptyAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "n1")
	l, err := Open(dir, Options{MayBeAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Absent() || l.LastIndex() != 0 {
		t.Fatalf("absent=%v last=%d, want an empty absent log", l.Absent(), l.LastIndex())
	}
	if err := l.Write(1, []byte("x")); !errors.Is(err, ErrAbsent) {
		t.Fatalf("write to an absent log: %v, want ErrAbsent", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening an absent log created %s", dir)
	}
}

// The store caches handles by path; a cached absent handle must give way
// once a writer has created the directory, or readers never see the writes.
func TestTheStoreReplacesAnAbsentHandleOnceTheDirExists(t *testing.T) {
	s := NewStore()
	defer s.Close()
	dir := filepath.Join(t.TempDir(), "n1")
	opts := Options{MayBeAbsent: true}
	first, err := s.Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Open(dir, opts)
	if err != nil || again != first {
		t.Fatalf("second open of a still-absent dir: %v, same=%v", err, again == first)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Absent() {
		t.Fatal("the store kept the absent handle after the dir appeared")
	}
	if err := fresh.Write(1, []byte("x")); err != nil {
		t.Fatal(err)
	}
}
