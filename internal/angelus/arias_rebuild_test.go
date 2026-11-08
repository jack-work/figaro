package angelus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/outfit"
)

// The outfit's content version is kept between listings, so it must move
// the moment the outfit's file does: a stale version would show every aria
// as on the current outfit after an edit.
func TestAListedOutfitVersionFollowsAnEditToTheOutfit(t *testing.T) {
	cfg := t.TempDir()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(os.MkdirAll(filepath.Join(cfg, "outfits"), 0o700))
	file := filepath.Join(cfg, "outfits", "perf.toml")
	require(os.WriteFile(file, []byte("[system]\nmodel = \"a\"\n"), 0o600))
	of := outfit.New(cfg)
	of.SetStaleWindow(0)
	h := &handlers{outfitter: of}

	first, _ := h.currentOutfitHash("perf")
	if first == "" {
		t.Fatal("the outfit does not resolve")
	}
	if again, _ := h.currentOutfitHash("perf"); again != first {
		t.Fatalf("an unchanged outfit hashed %q then %q", first, again)
	}
	require(os.WriteFile(file, []byte("[system]\nmodel = \"bb\"\n"), 0o600))
	if edited, _ := h.currentOutfitHash("perf"); edited == first {
		t.Fatalf("the outfit was edited and still lists as %q", first)
	}
	h.outfitter = outfit.New(cfg)
	h.outfitter.SetStaleWindow(0)
	if got, _ := h.currentOutfitHash("perf"); got == first {
		t.Fatalf("a reloaded outfitter at the same generation took the old outfitter's %q", first)
	}
}
