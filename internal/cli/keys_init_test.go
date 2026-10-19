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
