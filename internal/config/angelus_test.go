package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAngelusPathEntriesMustBeAbsoluteDirectories(t *testing.T) {
	for _, bad := range []string{"bin", "/usr/bin:/bin", "~user/bin"} {
		c := Config{Angelus: AngelusConfig{Path: []string{"/usr/bin", bad}}}
		if c.validateAngelus() == nil {
			t.Errorf("angelus.path entry %q was accepted", bad)
		}
	}
	home, _ := os.UserHomeDir()
	l := &Loaded{Config: Config{Angelus: AngelusConfig{Path: []string{"~/go/bin", "/usr/bin"}}}}
	if err := l.Config.validateAngelus(); err != nil {
		t.Fatal(err)
	}
	got := l.AngelusPath()
	if len(got) != 2 || got[0] != filepath.Join(home, "go/bin") || got[1] != "/usr/bin" {
		t.Fatalf("AngelusPath = %q", got)
	}
}
