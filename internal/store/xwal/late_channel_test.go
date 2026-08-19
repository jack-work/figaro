package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

// An ancestor can lack a directory in a channel: one added after it
// existed, or cleared. Opening that ancestor's log CREATES it, and an empty
// log owns its numbering from 1 -- so the ancestor stops delegating and the
// reader is severed from everything above it, silently, with fewer records
// rather than an error.
//
// The state is constructed rather than produced through the public API:
// creating a channel backfills a directory for every node, so no ordinary
// sequence of calls leaves the gap. That is why this is a guard, not a
// regression test -- the read path must not depend on backfill having
// covered every node, because the failure it produces is invisible.
func TestAMissingAncestorChannelDirIsPassedThroughNotCreated(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	g, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for i := 0; i < 3; i++ {
		if last, err = s.Append(string(g), "ir", 0, []byte(`{"g":1}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append(string(g), "notes", 1, []byte(`{"note":"g"}`), nil); err != nil {
		t.Fatal(err)
	}
	a, err := s.Fork(string(g), last-1) // interior: a inherits the note
	if err != nil {
		t.Fatal(err)
	}
	// a needs records of ITS OWN, or forking below its base forks the
	// grandparent instead and there is no chain to sever. (That is exactly
	// what the first version of this test did, and it made the test
	// unfailable: the node it removed was not in b's lineage at all.)
	var lastA uint64
	for i := 0; i < 3; i++ {
		if lastA, err = s.Append(a, "ir", 0, []byte(`{"a":1}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.Fork(a, lastA-1)
	if err != nil {
		t.Fatal(err)
	}
	if recs, err := s.RecordsFrom(b, "notes", 0, 0); err != nil || len(recs) != 1 {
		t.Fatalf("before the gap: b reads %d notes records (err %v), want 1", len(recs), err)
	}

	// Remove the MIDDLE node's directory, and reopen so nothing is cached.
	aNode, ok := s.HeadNode(a)
	if !ok {
		t.Fatal("no head node for a")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "notes", aNode)); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	recs, err := s2.RecordsFrom(b, "notes", 0, 0)
	if err != nil {
		t.Fatalf("read through the gap: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("b reads %d notes records through a missing ancestor dir, want 1: the chain was severed", len(recs))
	}
	if _, err := os.Stat(filepath.Join(dir, "notes", aNode)); !os.IsNotExist(err) {
		t.Errorf("the read CREATED %s (err %v); an empty log there claims base 1 and severs the chain", aNode, err)
	}
}
