package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCdPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ in, want string }{
		{"", home},
		{"~", home},
		{"~/x", filepath.Join(home, "x")},
		{".", cwd},
		{"sub", filepath.Join(cwd, "sub")},
		{"/tmp", "/tmp"},
	}
	for _, c := range cases {
		got, err := resolveCdPath(c.in)
		if err != nil {
			t.Fatalf("resolveCdPath(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("resolveCdPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
