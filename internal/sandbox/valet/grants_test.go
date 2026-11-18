package valet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func TestGrantsNameAFigaroByPrefixOrEveryone(t *testing.T) {
	tab := Table{Grants: []Grant{
		{Figaro: "3006ff4b", Path: "/w/a", Mode: "rw", Granted: t0, Expires: t0.Add(time.Hour)},
		{Figaro: Anyone, Path: "/w/b", Mode: "ro", Granted: t0, Expires: t0.Add(time.Hour)},
		{Figaro: "3006ff4b42ca", Path: "/w/c", Mode: "ro", Granted: t0, Expires: t0.Add(time.Minute)},
	}}
	paths := func(gs []Grant) string {
		var out []string
		for _, g := range gs {
			out = append(out, g.Path)
		}
		return strings.Join(out, ",")
	}
	if got := paths(tab.For("3006ff4b42cafe", t0, DefaultMaxGrant)); got != "/w/a,/w/b,/w/c" {
		t.Fatalf("named figaro sees %s", got)
	}
	if got := paths(tab.For("44eb6388", t0, DefaultMaxGrant)); got != "/w/b" {
		t.Fatalf("another figaro sees %s", got)
	}
	if got := paths(tab.For("3006ff4b42cafe", t0.Add(2*time.Minute), DefaultMaxGrant)); got != "/w/a,/w/b" {
		t.Fatalf("after a minute the figaro sees %s", got)
	}
	if got := paths(tab.For("3006ff4b42cafe", t0.Add(time.Minute/2), time.Minute/4)); got != "" {
		t.Fatalf("past the valet's cap the figaro sees %s", got)
	}
}

func TestAShortPrefixNamesNobody(t *testing.T) {
	g := Grant{Figaro: "3006", Path: "/w", Mode: "ro", Granted: t0, Expires: t0.Add(time.Hour)}
	if g.Names("3006ff4b42cafe") {
		t.Fatal("a four character prefix named a figaro")
	}
}

func TestTheTableRoundTripsPrivately(t *testing.T) {
	state := filepath.Join(t.TempDir(), "valet")
	var tab Table
	tab.Add(Grant{Figaro: Anyone, Path: "/w/a", Mode: "ro", Granted: t0, Expires: t0.Add(time.Hour)})
	tab.Add(Grant{Figaro: Anyone, Path: "/w/a", Mode: "rw", Granted: t0, Expires: t0.Add(2 * time.Hour)})
	tab.Add(Grant{Figaro: "3006ff4b", Path: "/w/a", Mode: "ro", Granted: t0, Expires: t0.Add(time.Hour)})
	if err := tab.Save(state); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{state: 0o700, GrantsPath(state): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want %v", path, fi.Mode().Perm(), err, want)
		}
	}
	back, err := Load(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Grants) != 2 || !back.Grants[0].Write() || !back.Grants[0].Expires.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("loaded %+v", back.Grants)
	}
	if n := back.Revoke(Anyone, ""); n != 1 || len(back.Grants) != 1 || back.Grants[0].Figaro != "3006ff4b" {
		t.Fatalf("revoked %d, left %+v", n, back.Grants)
	}
	if n := back.Prune(t0.Add(time.Hour), DefaultMaxGrant); n != 1 || len(back.Grants) != 0 {
		t.Fatalf("pruned %d, left %+v", n, back.Grants)
	}
	if empty, err := Load(filepath.Join(state, "none")); err != nil || len(empty.Grants) != 0 {
		t.Fatalf("a missing table: %+v %v", empty, err)
	}
}

func TestARequestBecomesACanonicalGrantOrIsRefused(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home", "alice")
	tree := filepath.Join(home, "dev", "figaro")
	state := filepath.Join(home, ".local", "state", "figaro-valet")
	for _, d := range []string{tree, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(tree, link); err != nil {
		t.Fatal(err)
	}
	base := Request{Figaro: "3006ff4b42ca", Path: link, Mode: "rw", For: 8 * time.Hour, Max: DefaultMaxGrant, Home: home, Guarded: []string{state}}
	g, err := base.Grant(t0)
	if err != nil {
		t.Fatal(err)
	}
	if g.Path != tree || !g.Expires.Equal(t0.Add(8*time.Hour)) || !g.Write() {
		t.Fatalf("grant %+v", g)
	}
	refused := map[string]func(*Request){
		"short id":       func(r *Request) { r.Figaro = "3006" },
		"glob id":        func(r *Request) { r.Figaro = "3006ff4b*" },
		"mode":           func(r *Request) { r.Mode = "rwx" },
		"no time":        func(r *Request) { r.For = 0 },
		"too long":       func(r *Request) { r.For = 8 * 24 * time.Hour },
		"home":           func(r *Request) { r.Path = home },
		"above home":     func(r *Request) { r.Path = filepath.Dir(home) },
		"root":           func(r *Request) { r.Path = "/" },
		"the table":      func(r *Request) { r.Path = state },
		"holds table":    func(r *Request) { r.Path = filepath.Join(home, ".local") },
		"missing":        func(r *Request) { r.Path = filepath.Join(home, "nope") },
		"inside a table": func(r *Request) { os.MkdirAll(filepath.Join(state, "x"), 0o700); r.Path = filepath.Join(state, "x") },
	}
	for name, mutate := range refused {
		r := base
		mutate(&r)
		if g, err := r.Grant(t0); err == nil {
			t.Errorf("%s: granted %+v", name, g)
		}
	}
	every := base
	every.Figaro = Anyone
	if _, err := every.Grant(t0); err != nil {
		t.Fatalf("a grant to every figaro: %v", err)
	}
}
