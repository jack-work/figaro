package config

import (
	"os"
	"path/filepath"
	"strings"
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
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\npurge_grace = \"1h\"\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\npurge_grace = \"an hour\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\npurge_grace = \"-1h\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\noutfits = [\"*\"]\nproviders = [\"*\"]\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\noutfits = [\"*\", \"judged\"]\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\nproviders = [\"mock\", \"*\"]\n", false},
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

// A doored box without both lists could mint undressed or on any provider,
// so its door must stay shut; a box with no door needs no lists.
func TestABoxDoorNeedsBothLists(t *testing.T) {
	for _, c := range []struct {
		extra   string
		door    bool
		refused string
	}{
		{"", true, "outfits or providers"},
		{"outfits = [\"judged\"]\n", true, "providers"},
		{"providers = [\"mock\"]\n", true, "outfits"},
		{"outfits = [\"judged\"]\nproviders = [\"mock\"]\n", true, ""},
		{"outfits = [\"*\"]\nproviders = [\"*\"]\n", true, ""},
		{"outfits = [\"*\"]\n", true, "providers"},
		{"", false, ""},
	} {
		body := "[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n" + c.extra
		if c.door {
			body += "door = \"/run/door/judge.sock\"\n"
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(dir)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		err = l.BoxDoorWithoutLists("judge")
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%q: refused: %v", body, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), "no "+c.refused+" list")):
			t.Errorf("%q: want the door refused for want of %s, got %v", body, c.refused, err)
		}
	}
}
