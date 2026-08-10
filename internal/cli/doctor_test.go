package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// gcFixture builds an isolated store root: a manifest listing channels and
// on-disk dirs for each of dirs. FIGARO_STATE_DIR points GC at it and
// FIGARO_RUNTIME_DIR at a socket-less dir so the "angelus is running" guard
// falls through. Returns the arias root.
func gcFixture(t *testing.T, channels, dirs []string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FIGARO_STATE_DIR", root)
	t.Setenv("FIGARO_RUNTIME_DIR", filepath.Join(root, "run"))

	chs := make([]map[string]any, 0, len(channels))
	for _, name := range channels {
		chs = append(chs, map[string]any{"name": name, "kind": "log"})
	}
	man, err := json.Marshal(map[string]any{"main": "ir", "codec": "jsonl", "channels": chs})
	if err != nil {
		t.Fatal(err)
	}
	arias := filepath.Join(root, "arias")
	if err := os.MkdirAll(arias, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arias, "xwal.json"), man, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		p := filepath.Join(arias, filepath.FromSlash(d))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "seg.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return arias
}

func manifestChannels(t *testing.T, arias string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(arias, "xwal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Channels []struct {
			Name string `json:"name"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(man.Channels))
	for _, c := range man.Channels {
		got = append(got, c.Name)
	}
	return got
}

// live channels must survive; dead ones must leave both the manifest and disk.
// translations-v2/* is the critical non-regression: it must NOT be swept by the
// legacy translations/ prefix rule.
func TestDoctorGCPrunesDeadChannels(t *testing.T) {
	channels := []string{"ir", "form", "turn-wal", "translations/anthropic", "translations-v2/anthropic"}
	dirs := []string{"ir", "form", "turn-wal", "_live", "translations/anthropic", "translations-v2/anthropic"}
	arias := gcFixture(t, channels, dirs)

	if err := runDoctorGC(false); err != nil {
		t.Fatalf("gc: %v", err)
	}

	want := []string{"ir", "form", "translations-v2/anthropic"}
	if got := manifestChannels(t, arias); !slices.Equal(got, want) {
		t.Errorf("manifest channels = %v, want %v", got, want)
	}
	for _, d := range []string{"turn-wal", "_live", "translations/anthropic"} {
		if _, err := os.Stat(filepath.Join(arias, filepath.FromSlash(d))); !os.IsNotExist(err) {
			t.Errorf("%s: still on disk", d)
		}
	}
	for _, d := range []string{"ir", "form", "translations-v2/anthropic"} {
		if _, err := os.Stat(filepath.Join(arias, filepath.FromSlash(d))); err != nil {
			t.Errorf("%s: should have survived: %v", d, err)
		}
	}
}

// --dry-run reports but mutates nothing.
func TestDoctorGCDryRunMutatesNothing(t *testing.T) {
	arias := gcFixture(t, []string{"ir", "turn-wal"}, []string{"ir", "turn-wal", "_live"})

	if err := runDoctorGC(true); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}

	if got, want := manifestChannels(t, arias), []string{"ir", "turn-wal"}; !slices.Equal(got, want) {
		t.Errorf("manifest mutated: got %v, want %v", got, want)
	}
	for _, d := range []string{"turn-wal", "_live"} {
		if _, err := os.Stat(filepath.Join(arias, d)); err != nil {
			t.Errorf("%s: dry run deleted it: %v", d, err)
		}
	}
}

// A store with nothing dead is left strictly alone.
func TestDoctorGCCleanStoreNoOp(t *testing.T) {
	arias := gcFixture(t, []string{"ir", "translations-v2/anthropic"}, []string{"ir", "translations-v2/anthropic"})

	if err := runDoctorGC(false); err != nil {
		t.Fatalf("gc: %v", err)
	}

	if got, want := manifestChannels(t, arias), []string{"ir", "translations-v2/anthropic"}; !slices.Equal(got, want) {
		t.Errorf("manifest = %v, want %v", got, want)
	}
}
