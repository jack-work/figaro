package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValetGrantRefusesWhatTheValetHides(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	err = runValetGrant(filepath.Join(home, ".local", "state", "figaro-valet"), "*", ssh, "ro", "1h", "")
	if err == nil || !strings.Contains(err.Error(), "keeps from every tool") {
		t.Fatalf("granting ~/.ssh: %v", err)
	}
}

func TestValetServeCanonicalizesItsHides(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	v, err := valetFromFlags(filepath.Join(dir, "state"), "", "", "", link)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(v.Hide, real) || slices.Contains(v.Hide, link) {
		t.Errorf("hides %q, want %s in place of %s", v.Hide, real, link)
	}
}
