package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
