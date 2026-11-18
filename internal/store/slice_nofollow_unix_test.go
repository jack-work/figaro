//go:build unix

package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The import copies a person's home store as root: it walks with Lstat, then
// opens. A file swapped for a link between the two must not be followed, or
// the copy would carry whatever root can read (a key, a shadow file) into the
// store the daemon owns.
func TestACopiedFileSwappedForALinkAfterTheWalkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("root only"), 0o600); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dir, "segment")
	if err := os.WriteFile(from, []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	walked, err := os.Lstat(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(from); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, from); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "copied")
	if err := copyFile(from, to, walked); err == nil {
		t.Fatal("copied through a link planted after the walk")
	}
	if got, err := os.ReadFile(to); err == nil && string(got) == "root only" {
		t.Fatal("the planted link's target landed in the copy")
	}
}
