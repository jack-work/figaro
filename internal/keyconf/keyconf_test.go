package keyconf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileIsTheDefaultMap(t *testing.T) {
	rows, warnings, err := Load(filepath.Join(t.TempDir(), "keys.toml"))
	if err != nil || rows != nil || warnings != nil {
		t.Fatalf("got %v %v %v, want three nils", rows, warnings, err)
	}
}

func TestLoad_NamesKeysNothingReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	os.WriteFile(path, []byte("[[key]]\nchord = \"J\"\naction = \"half-down\"\ncolour = \"blue\"\n"), 0o600)
	rows, warnings, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Chord != "J" || rows[0].Action != "half-down" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(warnings) != 1 {
		t.Fatalf("a key nothing reads must be named out loud, got %v", warnings)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	want := []Row{{Chord: "J", Action: "half-down"}, {Chord: "M-m", Mode: "all", Action: "none"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	rows := []Row{{Chord: "J", Action: "half-down"}}
	rows = Upsert(rows, Row{Chord: "J", Action: "line-down"})
	if len(rows) != 1 || rows[0].Action != "line-down" {
		t.Fatalf("upsert replaced the wrong thing: %+v", rows)
	}
	rows = Upsert(rows, Row{Chord: "K", Action: "line-up"})
	if rows = Remove(rows, "J", ""); len(rows) != 1 || rows[0].Chord != "K" {
		t.Fatalf("remove took the wrong row: %+v", rows)
	}
}
