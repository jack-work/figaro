package xwal

import (
	"path/filepath"
	"testing"
)

// Kinds land in the node marker at spawn, survive a reopen, and come back
// through List/ListLight without opening a single log — that is what lets
// a consumer split "form" listings from "conversation" listings on the
// cheap path.
func TestSpawnKindsRecordedAndListed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, unkeyedCfg())
	if err != nil {
		t.Fatal(err)
	}
	form, err := f.SpawnUnderRootKind("form")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := f.SpawnChildKind(form, "conversation")
	if err != nil {
		t.Fatal(err)
	}
	subform, err := f.SpawnChildKind(form, "form")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	g, err := openTrunks(dir, unkeyedCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, g)
	want := map[string]string{
		form:    "form",
		conv:    "conversation",
		subform: "form",
		legacy:  "conversation",
	}
	for name, list := range map[string][]TrunkInfo{"List": g.List(), "ListLight": g.ListLight()} {
		got := map[string]string{}
		for _, ti := range list {
			got[ti.ID] = ti.Kind
		}
		for id, kind := range want {
			if got[id] != kind {
				t.Errorf("%s: trunk %s kind = %q, want %q", name, id, got[id], kind)
			}
		}
	}
}

// The heart of "bind forks, nothing converts": a live form trunk hosts
// children of another kind while staying fully appendable. A child
// snapshots the parent's channels at the fork point; later parent patches
// belong to the parent alone; a second child sees them.
func TestSpawnChildKindParentStaysAppendable(t *testing.T) {
	f, err := createTrunks(filepath.Join(t.TempDir(), "f"), unkeyedCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)

	form, err := f.SpawnUnderRootKind("form")
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := MapSetPatch([]string{"k0"}, []byte(`0`))
	if _, err := f.AppendChannel(string(form), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	// The form needs a main record so the child's fork base covers the
	// patch (the cursor stamp lives on main; figaro's writeBirth writes
	// exactly this pairing).
	if _, _, err := f.Append(form, 0, []byte(`"birth"`), nil); err != nil {
		t.Fatal(err)
	}

	child1, err := f.SpawnChildKind(form, "conversation")
	if err != nil {
		t.Fatal(err)
	}

	// PARENT LIVES ON: patch it after the child exists.
	patch, _ = MapSetPatch([]string{"k1"}, []byte(`1`))
	if _, err := f.AppendChannel(string(form), "chalkboard", 0, patch, nil); err != nil {
		t.Fatalf("parent append after spawn: %v", err)
	}
	if _, _, err := f.Append(form, 0, []byte(`"post-spawn"`), nil); err != nil {
		t.Fatalf("parent main append after spawn: %v", err)
	}

	child2, err := f.SpawnChildKind(form, "conversation")
	if err != nil {
		t.Fatal(err)
	}

	board := func(trunk TrunkID) string {
		t.Helper()
		x, err := f.Head(trunk)
		if err != nil {
			t.Fatal(err)
		}
		defer x.Close()
		var last uint64
		for _, c := range x.Channels() {
			if c.Name == "chalkboard" {
				last = c.Last
			}
		}
		st, err := x.StateAt("chalkboard", last)
		if err != nil {
			t.Fatal(err)
		}
		return string(st)
	}

	if got, want := board(child1), `{"k0":0}`; got != want {
		t.Errorf("child1 board = %s, want %s (parent patches after the fork must not leak in)", got, want)
	}
	if got, want := board(child2), `{"k0":0,"k1":1}`; got != want {
		t.Errorf("child2 board = %s, want %s (a later fork takes the later base)", got, want)
	}
	if got, want := board(form), `{"k0":0,"k1":1}`; got != want {
		t.Errorf("parent board = %s, want %s (spawning children must not disturb the parent)", got, want)
	}
}
