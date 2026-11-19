package xwal

import (
	"path/filepath"
	"testing"
)

// A spawn that leaves its .node directory entry to the node's first main
// segment is acknowledged only after CommitBirth: the segment's directory
// sync covers the .node rename in the same directory, and with no segment
// CommitBirth syncs the directory itself. Nothing may stay pending once the
// caller commits or the store closes.
func TestABirthLeftToItsFirstSegmentIsSyncedBeforeItIsCommitted(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1, NoBackgroundFlush: true})
	if err != nil {
		t.Fatal(err)
	}
	pending := func(trunk string) bool {
		t.Helper()
		s.births.mu.Lock()
		defer s.births.mu.Unlock()
		_, ok := s.births.dirs[filepath.Clean(s.irDir(s.head(trunk)))]
		return ok
	}

	written, err := s.SpawnUnderRootKind("conversation", CommitWithFirstSegment())
	if err != nil {
		t.Fatal(err)
	}
	if !pending(written) {
		t.Fatal("a spawn left to its first segment synced its directory anyway: no fsync saved")
	}
	lt, err := s.Append(written, "main", 0, []byte(`"birth"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteChannelThrough(written, "main", lt); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncChannelThrough(written, "main", lt); err != nil {
		t.Fatal(err)
	}
	if pending(written) {
		t.Fatal("the first main segment's directory sync did not cover the .node rename")
	}

	var synced []string
	s.testSyncNodeDir = func(dir string) { synced = append(synced, filepath.Clean(dir)) }
	if err := s.CommitBirth(written); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 0 {
		t.Fatalf("CommitBirth synced %v again after the segment's sync covered it", synced)
	}
	bare, err := s.SpawnUnderRootKind("conversation", CommitWithFirstSegment())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitBirth(bare); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean(s.irDir(s.head(bare))); len(synced) != 1 || synced[0] != want {
		t.Fatalf("CommitBirth of a node with no segment synced %v, want %s", synced, want)
	}
	if pending(bare) {
		t.Fatal("CommitBirth left a node with no segment uncommitted")
	}

	left, err := s.SpawnUnderRootKind("conversation", CommitWithFirstSegment())
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Clean(s.irDir(s.head(left)))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || synced[1] != key {
		t.Fatalf("close synced %v, want %s's directory last", synced, key)
	}

	plain, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1, NoBackgroundFlush: true})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	id, err := plain.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	plain.births.mu.Lock()
	n := len(plain.births.dirs)
	plain.births.mu.Unlock()
	if n != 0 {
		t.Fatalf("a spawn that did not ask to wait for its segment (%s) left its directory unsynced", id)
	}
}

// A fork of a node whose .node is not yet durable must make the parent
// durable first: otherwise a crash can keep the child's .node, which names
// the parent, and lose the parent's.
func TestAForkOfAnUncommittedNodeCommitsTheParentFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1, NoBackgroundFlush: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	markers := func() int {
		m, _ := filepath.Glob(filepath.Join(dir, "main", "*", nodeMarkerName))
		return len(m)
	}
	parent, err := s.SpawnUnderRootKind("conversation", CommitWithFirstSegment())
	if err != nil {
		t.Fatal(err)
	}
	parentDir := filepath.Clean(s.irDir(s.head(parent)))
	before := markers()
	synced := map[string]int{}
	s.testSyncNodeDir = func(d string) { synced[filepath.Clean(d)] = markers() }
	if _, err := s.ForkTail(parent); err != nil {
		t.Fatal(err)
	}
	at, ok := synced[parentDir]
	if !ok {
		t.Fatal("the fork wrote its child's .node while the parent's was still unsynced")
	}
	if at != before {
		t.Fatalf("the parent's directory was synced after the child's .node appeared (%d markers, %d before)", at, before)
	}
}
