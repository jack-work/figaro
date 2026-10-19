package cli

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	figaro "github.com/jack-work/figaro"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/keyconf"
)

// init installs the embedded example byte for byte, and will not overwrite a
// file the reader already has.
func TestKeysInitWritesTheShippedExample(t *testing.T) {
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	var out bytes.Buffer
	old := stdout
	stdout = &out
	t.Cleanup(func() { stdout = old })

	if err := runKeys(loaded, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "keys.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := fs.ReadFile(figaro.Skills, keysExample)
	if !bytes.Equal(got, want) || len(want) == 0 {
		t.Fatal("init did not write the shipped example")
	}
	err = runKeys(loaded, []string{"init"})
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("a second init must refuse: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "keys.toml")); !bytes.Equal(after, got) {
		t.Fatal("a refused init changed the file")
	}
}

// reset forgets the bindings; the display table is not a binding.
func TestKeysResetKeepsDisplay(t *testing.T) {
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	old := stdout
	stdout = &bytes.Buffer{}
	t.Cleanup(func() { stdout = old; applyUserKeys(keyconf.File{}) })
	path := filepath.Join(dir, "keys.toml")
	if err := os.WriteFile(path, []byte("[display]\nids = \"short\"\n\n[bind.user]\nJ = \"half-down\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runKeys(loaded, []string{"reset"}); err != nil {
		t.Fatal(err)
	}
	f, _, err := keyconf.Load(path)
	if err != nil || f.HasBindings() || f.Display.IDs != "short" {
		t.Fatalf("after reset: %+v %v", f, err)
	}
}
