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

// figaro keys bind rewrote keys.toml from what it parsed, so the shipped
// example's comments (and the reader's own) were gone after the first bind.
// A bind edits the file in place.
func TestKeysBindKeepsTheFilesComments(t *testing.T) {
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	var out bytes.Buffer
	old := stdout
	stdout = &out
	t.Cleanup(func() { stdout = old })
	t.Cleanup(func() { applyUserKeys(keyconf.File{}) })

	if err := runKeys(loaded, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keys.toml")
	orig, _ := os.ReadFile(path)
	if err := runKeys(loaded, []string{"bind", "^E", "scroll-down", "list"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	var comments int
	for _, line := range strings.Split(string(orig), "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "#") {
			comments++
			if !strings.Contains(string(got), line) {
				t.Errorf("bind dropped the comment %q", line)
			}
		}
	}
	if comments == 0 {
		t.Fatal("fixture: the shipped example has no comments")
	}
	if !strings.Contains(string(got), `"^E" = "scroll-down"`) {
		t.Errorf("the binding was not written:\n%s", got)
	}
}
