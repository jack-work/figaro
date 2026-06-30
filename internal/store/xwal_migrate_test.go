package store

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyTree recursively copies src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}

// TestMigrate_LiveCopy migrates a COPY of an old-layout arias dir (the live
// store works fine — the test copies it first) and asserts the new layout.
// Set FIGARO_MIGRATE_TEST_SRC to an old-layout arias dir (e.g.
// ~/.local/state/figaro/arias) to run it.
func TestMigrate_LiveCopy(t *testing.T) {
	src := os.Getenv("FIGARO_MIGRATE_TEST_SRC")
	if src == "" {
		t.Skip("set FIGARO_MIGRATE_TEST_SRC to an old-layout arias dir")
	}
	dir := filepath.Join(t.TempDir(), "arias")
	copyTree(t, src, dir)

	// Sanity: the copy is old-layout (root has a .trunk marker).
	if !fileExists(filepath.Join(dir, "ir", ".trunk")) {
		t.Skip("source is not an old-layout store (no root .trunk)")
	}

	st, err := OpenXwalStore(dir) // triggers migrateToStumps
	if err != nil {
		t.Fatalf("open+migrate: %v", err)
	}

	// 1. Root sheds its marker.
	if fileExists(filepath.Join(dir, "ir", ".trunk")) {
		t.Error("root still carries a .trunk marker after migration")
	}
	// 2. Translations channel dropped (re-backfills lazily).
	if fileExists(filepath.Join(dir, "translations")) {
		t.Error("translations dir should be removed by migration")
	}
	mani, _ := os.ReadFile(filepath.Join(dir, "xwal.json"))
	if strings.Contains(string(mani), "translations/") {
		t.Error("manifest still references a translations channel")
	}
	// 3. Stumps are named + markerless; conversations keep their markers.
	stumps := st.trunks.Stumps()
	if len(stumps) == 0 {
		t.Fatal("no stumps after migration")
	}
	for _, sName := range stumps {
		if !strings.Contains(sName.Name, "@") {
			t.Errorf("stump %q not named <loadout>@<ver>", sName.Name)
		}
		if fileExists(filepath.Join(dir, "ir", sName.Name, ".trunk")) {
			t.Errorf("stump %q still carries a .trunk marker", sName.Name)
		}
	}
	// 4. Nodes() yields a null root + a loadout per stump + conversations.
	var nNull, nLoadout, nConv int
	for _, n := range st.Nodes() {
		switch n.Kind {
		case string(kindNull):
			nNull++
		case string(kindLoadout):
			nLoadout++
		case string(kindConversation):
			nConv++
		}
	}
	if nNull != 1 {
		t.Errorf("want exactly 1 null root, got %d", nNull)
	}
	if nLoadout != len(stumps) {
		t.Errorf("loadout count %d != stump count %d", nLoadout, len(stumps))
	}
	if nConv == 0 {
		t.Error("expected at least one conversation after migration")
	}
	t.Logf("migrated: %d loadouts, %d conversations", nLoadout, nConv)

	// 5. Idempotent: a second open is a no-op and still resolves.
	if _, err := OpenXwalStore(dir); err != nil {
		t.Fatalf("re-open after migration: %v", err)
	}

	// 6. Every conversation head opens and reads end-to-end.
	for _, n := range st.Nodes() {
		if n.Kind != string(kindConversation) {
			continue
		}
		x, err := st.OpenNode(n.ID)
		if err != nil {
			t.Errorf("open conversation %s: %v", n.ID, err)
			continue
		}
		x.Close()
	}
}
