package outfit

import (
	"os"
	"path/filepath"
	"testing"
)

// A file left in the pre-rename loadouts/ still resolves; outfits/ wins ties.
func TestLoadResolvesLegacyLoadoutsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "loadouts"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(sub, name, model string) {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "[system]\nmodel = \"" + model + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, sub, name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("loadouts", "only-legacy", "m1")
	write("loadouts", "shadowed", "old")
	write("outfits", "shadowed", "new")

	for _, tc := range []struct{ name, want string }{
		{"only-legacy", `"m1"`},
		{"shadowed", `"new"`},
	} {
		patch, err := New(dir).Load(tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := string(patch.Set["system.model"]); got != tc.want {
			t.Fatalf("%s: system.model = %s, want %s", tc.name, got, tc.want)
		}
	}
}
