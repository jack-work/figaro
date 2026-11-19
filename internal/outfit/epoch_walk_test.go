package outfit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
