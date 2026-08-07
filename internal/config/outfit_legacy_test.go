package config

import (
	"os"
	"path/filepath"
	"testing"
)

// config.toml files written before the outfit rename say default_loadout.
func TestLoadAcceptsLegacyDefaultLoadoutKey(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"legacy only", "default_loadout = \"legacy\"\n", "legacy"},
		{"both spellings", "default_loadout = \"old\"\ndefault_outfit = \"new\"\n", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			l, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if l.Config.DefaultOutfit != tc.want {
				t.Fatalf("DefaultOutfit = %q, want %q", l.Config.DefaultOutfit, tc.want)
			}
		})
	}
}

// Both directories are read, so a half-migrated config dir still works.
func TestOutfitDirsUnionBothLegacyAndCanonical(t *testing.T) {
	dir := t.TempDir()
	for sub, names := range map[string][]string{
		"loadouts": {"alpha", "shared"},
		"outfits":  {"beta", "shared"},
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, sub, n+".toml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	l := &Loaded{ConfigDir: dir}

	want := []string{"alpha", "beta", "shared"}
	got := l.ListOutfits()
	if len(got) != len(want) {
		t.Fatalf("ListOutfits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListOutfits = %v, want %v", got, want)
		}
	}

	for _, tc := range []struct{ name, sub string }{
		{"alpha", "loadouts"}, // only legacy has it
		{"shared", "outfits"}, // canonical wins
		{"absent", "outfits"}, // a fresh name lands canonically
	} {
		if p := l.OutfitPath(tc.name); p != filepath.Join(dir, tc.sub, tc.name+".toml") {
			t.Fatalf("OutfitPath(%q) = %s, want it under %s", tc.name, p, tc.sub)
		}
	}
}
