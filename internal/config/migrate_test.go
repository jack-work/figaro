package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateMovesLoadoutsDirAndRenamesKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "loadouts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "loadouts", "legacy.toml"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "# a comment worth keeping\ndefault_loadout = \"legacy\"   # and this one\ntrunks = true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	changes, err := MigrateLegacyOutfits(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want the dir move and the key rename", changes)
	}
	if _, err := os.Stat(filepath.Join(dir, "loadouts")); !os.IsNotExist(err) {
		t.Fatalf("legacy dir survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outfits", "legacy.toml")); err != nil {
		t.Fatalf("outfit did not travel: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# a comment worth keeping\ndefault_outfit = \"legacy\"   # and this one\ntrunks = true\n"
	if string(got) != want {
		t.Fatalf("config.toml =\n%q\nwant\n%q", got, want)
	}

	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.DefaultOutfit != "legacy" {
		t.Fatalf("DefaultOutfit = %q", l.Config.DefaultOutfit)
	}
	if again, err := MigrateLegacyOutfits(dir); err != nil || len(again) != 0 {
		t.Fatalf("not idempotent: %v %v", again, err)
	}
}

// Clobbering a real outfits/ or default_outfit is worse than leaving a stale
// legacy name the read paths already tolerate.
func TestMigrateDeclinesWhenBothSpellingsExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "loadouts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "outfits"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "default_loadout = \"old\"\ndefault_outfit = \"new\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	changes, err := MigrateLegacyOutfits(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %v, want none", changes)
	}
	if _, err := os.Stat(filepath.Join(dir, "loadouts")); err != nil {
		t.Fatalf("legacy dir should be left alone: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(got) != body {
		t.Fatalf("config.toml rewritten: %q", got)
	}
}
