package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRootIsAbsoluteOrUnset(t *testing.T) {
	for _, c := range []struct {
		root, want string
		ok         bool
	}{
		{"", "", true},
		{"/var/lib/figaro/workspaces/", "/var/lib/figaro/workspaces", true},
		{"workspaces", "", false},
	} {
		l := &Loaded{Config: Config{Sandbox: SandboxConfig{WorkspaceRoot: c.root}}}
		got, err := l.WorkspaceRoot()
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: %q, %v", c.root, got, err)
		}
	}
}

func TestBoxesLoadAndRefuseWhatTheyCannotBe(t *testing.T) {
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\ndoor = \"/run/door/judge.sock\"\nprofile = \"build\"\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n", true},
		{"[sandbox.boxes.local]\nexec = \"/x\"\n", false},
		{"[sandbox.boxes.bwrap]\nexec = \"/x\"\n", false},
		{"[sandbox.boxes.\"a@b\"]\nexec = \"/x\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"judge.sock\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\ndoor = \"door.sock\"\n", false},
		{"[sandbox.boxes.a]\nexec = \"/x\"\ndoor = \"/d\"\n[sandbox.boxes.b]\nexec = \"/y\"\ndoor = \"/d/\"\n", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(dir)
		if (err == nil) != c.ok {
			t.Errorf("%q: %v", c.body, err)
			continue
		}
		if c.ok {
			if b, ok := l.Box("judge"); ok && b.Exec != "/run/box/judge.sock" {
				t.Errorf("%q: box %+v", c.body, b)
			}
		}
	}
}
