package outfit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

func TestAnEpochWalksTheIndexOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "outfits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outfits", "seat.toml"), []byte("[system]\nmodel = \"seat\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := New(dir)
	o.SetStaleWindow(time.Hour)
	if _, err := o.Load("seat"); err != nil {
		t.Fatal(err)
	}
	ep := o.current()
	first := reflect.ValueOf(ep.index).Pointer()
	r := &reader{o: o, ep: ep}
	for _, name := range []string{"absent", "other", "seat"} {
		if _, _, _, err := o.resolveEntry(name, false, r); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if got := reflect.ValueOf(ep.index).Pointer(); got != first {
			t.Fatalf("asking for %q walked the index again inside one epoch", name)
		}
	}
}

func TestAFoldReadsThroughTheRootItsEpochWalked(t *testing.T) {
	base := t.TempDir()
	gen := func(model string) string {
		dir := filepath.Join(base, model)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "seat.toml"), []byte("[system]\nmodel = \""+model+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	link := filepath.Join(base, "managed")
	if err := os.Symlink(gen("gen-a"), link); err != nil {
		t.Fatal(err)
	}
	o := New(t.TempDir())
	o.SetStaleWindow(time.Hour)
	o.SetRoots(config.OutfitRoots{Dirs: []config.OutfitRoot{{Path: link, Managed: true}, {Path: filepath.Join(base, "user")}}})
	ep := o.current()
	if _, err := (&reader{o: o, ep: ep}).outfitIndex(); err != nil {
		t.Fatal(err)
	}

	tmp := link + ".new"
	if err := os.Symlink(gen("gen-b"), tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
	keys, err := o.foldIn(ep, "seat", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(keys["system.model"]); got != `"gen-a"` {
		t.Fatalf("system.model = %s: a fold must read the generation its epoch walked", got)
	}
	if n, _ := o.nodeFor(ep, "seat", false); n.path != filepath.Join(link, "seat.toml") {
		t.Fatalf("path = %s: shown through the root as configured", n.path)
	}
}
