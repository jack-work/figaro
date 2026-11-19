package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Gluck, 2026-10-14: outfit files may be organized into directories; the
// name stays the basename, one namespace.
func TestOutfitFilesWalksSubdirectories(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"outfits/core.toml",
		"outfits/decide/luna.toml",
		"outfits/decide/msft/msft.toml",
		"outfits/notes.md",
	)
	var visited []string
	files, err := defaultOutfitPaths(dir, func(d string) { visited = append(visited, d) })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"core": {filepath.Join(dir, "outfits/core.toml")},
		"luna": {filepath.Join(dir, "outfits/decide/luna.toml")},
		"msft": {filepath.Join(dir, "outfits/decide/msft/msft.toml")},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	// Every directory walked is reported, so a caller can notice a file
	// appearing in any of them later.
	sort.Strings(visited)
	wantDirs := []string{
		filepath.Join(dir, "outfits"),
		filepath.Join(dir, "outfits/decide"),
		filepath.Join(dir, "outfits/decide/msft"),
	}
	if !reflect.DeepEqual(visited, wantDirs) {
		t.Fatalf("visited = %v, want %v", visited, wantDirs)
	}
}

func TestOutfitFilesSkipsHiddenEntries(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"outfits/real.toml",
		"outfits/.git/HEAD.toml",
		"outfits/.draft.toml",
	)
	files, err := defaultOutfitPaths(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["real"] == nil {
		t.Fatalf("files = %v: hidden directories and files are not outfits", files)
	}
}

func TestOutfitFilesReportsEveryPathOfADuplicate(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, "outfits/jev.toml", "outfits/old/jev.toml")
	files, err := defaultOutfitPaths(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files["jev"]) != 2 {
		t.Fatalf("jev = %v: both copies must be reported so the caller can refuse the ambiguity", files["jev"])
	}
}

// loadouts/ is the pre-rename directory: still read, flat, and shadowed by
// outfits/. Gluck's own loadouts is a symlink TO outfits, which must not
// turn every outfit into a duplicate of itself.
func TestTheLegacyDirectoryIsShadowedNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, "outfits/core.toml", "outfits/decide/jev.toml")
	if err := os.Symlink("outfits", filepath.Join(dir, "loadouts")); err != nil {
		t.Fatal(err)
	}
	files, err := defaultOutfitPaths(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, paths := range files {
		if len(paths) != 1 {
			t.Fatalf("%s = %v: a symlinked loadouts/ duplicated it", name, paths)
		}
	}
}

func TestNoOutfitsDirectoryIsNoOutfits(t *testing.T) {
	files, err := defaultOutfitPaths(t.TempDir(), nil)
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
}

func TestListOutfitsAndOutfitPathSeeNestedFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, "outfits/core.toml", "outfits/decide/jev.toml")
	l := &Loaded{ConfigDir: dir}
	if got := l.ListOutfits(); !reflect.DeepEqual(got, []string{"core", "jev"}) {
		t.Fatalf("ListOutfits = %v", got)
	}
	if got := l.OutfitPath("jev"); got != filepath.Join(dir, "outfits/decide/jev.toml") {
		t.Fatalf("OutfitPath(jev) = %s", got)
	}
	if got := l.OutfitPath("new"); got != filepath.Join(dir, "outfits/new.toml") {
		t.Fatalf("a name that does not exist yet goes at the top: %s", got)
	}
}

func defaultOutfitPaths(dir string, visit func(string)) (map[string][]string, error) {
	idx, err := OutfitFiles(DefaultOutfitRoots(dir), visit)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for name, e := range idx {
		out[name] = e.Paths
	}
	return out, nil
}

func TestUnsetRootsIsTheOneConfigRoot(t *testing.T) {
	dir := t.TempDir()
	l := &Loaded{ConfigDir: dir}
	if got, want := l.OutfitRoots(), DefaultOutfitRoots(dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %+v, want %+v", got, want)
	}
	if got := l.OutfitsDir(); got != filepath.Join(dir, "outfits") {
		t.Fatalf("OutfitsDir = %s", got)
	}
}

func TestConfiguredRootsAreManagedUntilTheLast(t *testing.T) {
	dir := t.TempDir()
	home, _ := os.UserHomeDir()
	l := &Loaded{ConfigDir: dir, Config: Config{Outfits: OutfitsConfig{Roots: []string{"/etc/figaro/outfits", "~/x", "mine"}}}}
	want := OutfitRoots{Dirs: []OutfitRoot{
		{Path: "/etc/figaro/outfits", Managed: true},
		{Path: filepath.Join(home, "x"), Managed: true},
		{Path: filepath.Join(dir, "mine")},
	}}
	if got := l.OutfitRoots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %+v, want %+v", got, want)
	}
}

func TestOutfitRootsLoadFromTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[outfits]\nroots = [\"/m\", \"/u\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.OutfitRoots().Overlay(); got != "/u" {
		t.Fatalf("overlay = %s", got)
	}
}

func TestAConfigWhoseOutfitRootsAliasOrNestDoesNotLoad(t *testing.T) {
	for name, roots := range map[string]string{
		"same":    `["managed", "managed"]`,
		"nested":  `["managed", "managed/user"]`,
		"parent":  `["managed/inner", "managed"]`,
		"aliased": `["managed", "alias"]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "managed", "inner"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("managed", filepath.Join(dir, "alias")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[outfits]\nroots = "+roots+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "nest") {
				t.Fatalf("Load = %v: roots that alias or nest must refuse the config", err)
			}
		})
	}
}

func TestAnOutfitRootSwappedOntoAnotherIsAnIndexError(t *testing.T) {
	dir := t.TempDir()
	managed, user := filepath.Join(dir, "managed"), filepath.Join(dir, "user")
	writeTree(t, dir, "managed/seat.toml", "user/mine.toml")
	roots := OutfitRoots{Dirs: []OutfitRoot{{Path: managed, Managed: true}, {Path: user}}}
	if _, err := OutfitFiles(roots, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(user); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(managed, user); err != nil {
		t.Fatal(err)
	}
	if _, err := OutfitFiles(roots, nil); err == nil {
		t.Fatal("a user root linked onto the managed root must not index the managed files twice")
	}
}

func TestAWritePathThatResolvesIntoAManagedRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	managed, user := filepath.Join(dir, "managed"), filepath.Join(dir, "user")
	writeTree(t, dir, "managed/seat.toml")
	roots := OutfitRoots{Dirs: []OutfitRoot{{Path: managed, Managed: true}, {Path: user}}}
	if got, err := roots.InManagedRoot(filepath.Join(user, "fresh.toml")); err != nil || got != "" {
		t.Fatalf("InManagedRoot(user) = %q, %v", got, err)
	}
	if err := os.Symlink(managed, user); err != nil {
		t.Fatal(err)
	}
	if got, err := roots.InManagedRoot(filepath.Join(user, "fresh.toml")); err != nil || got != managed {
		t.Fatalf("InManagedRoot through a link = %q, %v: want %s", got, err, managed)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[outfits]\nroots = [\"managed\", \"elsewhere\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(managed, filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.OutfitWritePath("fresh"); err == nil {
		t.Fatal("OutfitWritePath resolved into the managed root")
	}
}

func TestABrokenLinkAtAManagedRootOrItsLockFailsClosed(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		dir := t.TempDir()
		managed, user := filepath.Join(dir, "managed"), filepath.Join(dir, "user")
		if err := os.Symlink(filepath.Join(dir, "gone"), managed); err != nil {
			t.Fatal(err)
		}
		writeTree(t, dir, "user/seat.toml")
		roots := OutfitRoots{Dirs: []OutfitRoot{{Path: managed, Managed: true}, {Path: user}}}
		if _, err := OutfitFiles(roots, nil); err == nil {
			t.Fatal("a dangling managed root read as no managed outfits, freeing every locked name")
		}
	})
	t.Run("lock", func(t *testing.T) {
		dir := t.TempDir()
		managed, user := filepath.Join(dir, "managed"), filepath.Join(dir, "user")
		writeTree(t, dir, "managed/seat.toml", "user/seat.toml")
		if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(managed, OutfitLockFile)); err != nil {
			t.Fatal(err)
		}
		roots := OutfitRoots{Dirs: []OutfitRoot{{Path: managed, Managed: true}, {Path: user}}}
		if _, err := OutfitFiles(roots, nil); err == nil {
			t.Fatal("a dangling .locked read as no lock")
		}
	})
	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		roots := OutfitRoots{Dirs: []OutfitRoot{{Path: filepath.Join(dir, "managed"), Managed: true}, {Path: filepath.Join(dir, "user")}}}
		if _, err := OutfitFiles(roots, nil); err != nil {
			t.Fatalf("a managed root that is simply absent is no outfits: %v", err)
		}
	})
}
