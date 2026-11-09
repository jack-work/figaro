package xwal

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Adopting a lineage whose owner cannot flush its buffered tail retires the
// owner's store, which fences the node for every Trunks on the root, so the
// adopter never reads the short tail from disk.
func TestAdoptingALineageWhoseOwnerCannotFlushFencesTheNode(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	s.syncDirty()
	peer, err := openTrunks(dir, s.Trunks.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	if _, err := s.Append(tr, "ir", 0, []byte(`{"i":0}`), nil); err != nil {
		t.Fatal(err)
	}
	node, _ := s.HeadNode(tr)
	s.Trunks.testHeadSync = func(key string) error {
		if key == node {
			return fmt.Errorf("input/output error")
		}
		return nil
	}
	defer func() { s.Trunks.testHeadSync = nil }()

	lineage := peer.rootLineage(string(tr))
	lineage.mu.Lock()
	if lineage.owner != s.Trunks {
		lineage.mu.Unlock()
		t.Fatal("the store's Trunks does not own the lineage it appended to")
	}
	peer.adoptLineage(string(tr), lineage)
	lineage.mu.Unlock()

	if holder := rootFenceHolder(peer.registryRoot, node); holder != s.Trunks {
		t.Fatalf("after a failed sync on adoption the node is fenced by %p, want the owner %p", holder, s.Trunks)
	}
}

// A lineage head whose own flush fails is not forgotten: it stays in the hot
// store so the fallback retire flushes it again and fences its node.
func TestALineageHeadThatCannotFlushIsNotForgotten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	key, ok := f.idx.Head(trunk)
	if !ok {
		t.Fatal("no head key")
	}
	f.testHeadSync = func(k string) error {
		if k == key {
			return fmt.Errorf("input/output error")
		}
		return nil
	}
	defer func() { f.testHeadSync = nil }()
	h := hotStoreOf(f)
	if f.forgetLineageHead(trunk) {
		t.Fatal("forgetLineageHead dropped a head whose flush failed")
	}
	f.hotMu.Lock()
	_, kept := h.heads[key]
	f.hotMu.Unlock()
	if !kept {
		t.Fatal("the head whose flush failed was removed from the hot store")
	}
}
