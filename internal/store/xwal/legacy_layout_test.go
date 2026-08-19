package xwal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The lazy read-path upgrade of the .from/.kind pair is gone. A build
// either understands a store or refuses it; there is no third path that
// half-reads an older one. So a store carrying the legacy pair must not
// open, and must open with everything after Flatten.
func TestLegacyPairIsRefusedThenMigrated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	if _, _, err := f.Append(trunk, 0, []byte(`"turn"`), nil); err != nil {
		t.Fatal(err)
	}
	key := f.head(string(trunk))
	nodeDir := f.irDir(key)
	n, ok := readNodeMarker(nodeDir)
	if !ok {
		t.Fatalf("no marker at %s", nodeDir)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind this node to the legacy pair, and the manifest to a store
	// with no layout stamp: what a build before the merge left behind.
	if err := os.Remove(filepath.Join(nodeDir, nodeMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncedFile(filepath.Join(nodeDir, legacyFromName),
		[]byte(n.from+"\n"+n.kind+"\n")); err != nil {
		t.Fatal(err)
	}
	if n.trunk != "" {
		if err := writeSyncedFile(filepath.Join(nodeDir, legacyTrunkName), []byte(n.trunk+"\n")); err != nil {
			t.Fatal(err)
		}
	}
	unstampManifest(t, dir)

	if _, err := openTrunks(dir, trunksCfg()); !errors.Is(err, ErrLegacyLayout) {
		t.Fatalf("open of a legacy store: got %v, want ErrLegacyLayout", err)
	}

	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}
	if m, ok := readNodeMarker(nodeDir); !ok || m.from != n.from || m.trunk != n.trunk || m.kind != n.kind {
		t.Fatalf("migrated marker %+v, want %+v (ok %v)", m, n, ok)
	}
	for _, legacy := range []string{legacyFromName, legacyTrunkName} {
		if _, err := os.Stat(filepath.Join(nodeDir, legacy)); !os.IsNotExist(err) {
			t.Errorf("%s survived the migration (err=%v)", legacy, err)
		}
	}
	g, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatalf("open after migration: %v", err)
	}
	cleanupTrunks(t, g)
	if _, ok := g.idx.Head(trunk); !ok {
		t.Error("the migrated node lost its head")
	}
}

// The refusal is the whole point of the stamp: a nested store must not
// open, least of all "successfully" with an almost-empty tree.
func TestOpenRefusesANestedStoreRatherThanReportingItEmpty(t *testing.T) {
	dir, before := buildNestedFixture(t)
	_, err := OpenStore(dir, testStoreOptions())
	if !errors.Is(err, ErrLegacyLayout) {
		t.Fatalf("open of a nested store: got %v, want ErrLegacyLayout", err)
	}
	if _, err := Flatten(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := len(s.List()); got != len(before.trunks) {
		t.Errorf("trunks after migration: %d, want %d", got, len(before.trunks))
	}
}

func unstampManifest(t *testing.T, dir string) {
	t.Helper()
	m, err := readManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Layout, m.UnstampedRecords = 0, false
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
}
