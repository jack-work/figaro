package xwal

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func hotStoreOf(t *Trunks) *trunkStore {
	t.hotMu.Lock()
	defer t.hotMu.Unlock()
	return t.hot
}

func spawnUnderRoot(t *testing.T, f *Trunks, n int) []TrunkID {
	t.Helper()
	ids := make([]TrunkID, 0, n)
	for i := 0; i < n; i++ {
		id, err := f.SpawnUnderRoot()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.Append(id, 0, []byte(fmt.Sprintf(`"seed %d"`, i)), nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A store built by one Trunks, closed, and reopened by another in the same
// process: the closed Trunks must not own the lineages, or every first head
// on a lineage retires the reader's whole hot store and reopens the root.
func TestAClosedTrunksOwnsNoLineage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	builder, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	ids := spawnUnderRoot(t, builder, 20)
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reader)
	var first *trunkStore
	for i, id := range ids {
		head, err := reader.Head(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := head.Close(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = hotStoreOf(reader)
			continue
		}
		if got := hotStoreOf(reader); got != first {
			t.Fatalf("head %d of %d replaced the reader's hot store", i+1, len(ids))
		}
	}
}

// Adopting a lineage forgets that lineage's head and cached logs only: the
// adopter keeps its hot store and every other lineage it holds.
func TestForgettingALineageHeadKeepsTheHotStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	ids := spawnUnderRoot(t, f, 3)
	for _, id := range ids {
		head, err := f.Head(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := head.Close(); err != nil {
			t.Fatal(err)
		}
	}
	before := hotStoreOf(f)
	key, ok := f.idx.Head(ids[1])
	if !ok {
		t.Fatal("no head key")
	}
	irDir := filepath.Join(f.root, "ir", fsName(key))
	if !before.store.Holds(irDir) {
		t.Fatal("the lineage's log was not cached before the forget")
	}

	if !f.forgetLineageHead(ids[1]) {
		t.Fatal("forgetLineageHead refused an unborrowed head")
	}
	if got := hotStoreOf(f); got != before {
		t.Fatal("forgetting one lineage replaced the hot store")
	}
	if before.store.Holds(irDir) {
		t.Fatal("the forgotten lineage's log is still cached")
	}
	f.hotMu.Lock()
	_, kept := before.heads[strings.Join([]string{key}, "\x00")]
	f.hotMu.Unlock()
	if kept {
		t.Fatal("the forgotten lineage's head is still loaded")
	}

	if _, _, err := f.Append(ids[1], 0, []byte(`"after"`), nil); err != nil {
		t.Fatal(err)
	}
	if got := headPayloads(t, f, ids[1]); got[len(got)-1] != `"after"` {
		t.Fatalf("reload after forget read %v", got)
	}
}

// A borrowed head is not forgotten; the caller falls back to retiring.
func TestABorrowedLineageHeadIsNotForgotten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	if f.forgetLineageHead(trunk) {
		t.Fatal("forgetLineageHead dropped a borrowed head")
	}
}

// A head still borrowed in a retired store keeps its node: forgetting the
// lineage would open a fresh head beside it, so adoption takes the fallback.
func TestARetiredBorrowedLineageHeadIsNotForgotten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	f.retireHot()
	if f.forgetLineageHead(trunk) {
		t.Fatal("forgetLineageHead ignored a head borrowed in a retired store")
	}
}
