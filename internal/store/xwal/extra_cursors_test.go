package xwal

import (
	"path/filepath"
	"testing"
)

// Extra cursor entries ride the main record's stamp beside the unkeyed
// channel positions: one map carries the node's own channels AND the
// caller's foreign positions (figaro's observed forms), and both come
// back on the decoded record.
func TestAppendMainCursorsMergesExtra(t *testing.T) {
	f, err := createTrunks(filepath.Join(t.TempDir(), "f"), unkeyedCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	trunk, err := f.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := MapSetPatch([]string{"own"}, []byte(`1`))
	if _, err := f.AppendChannel(string(trunk), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	_, lt, _, err := f.AppendCursors(trunk, []byte(`"turn"`), nil, map[string]uint64{
		"study:@r1": 7,
		"study:@r2": 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	rec, err := x.ReadAt(x.main, lt)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Cursors["chalkboard"] != 2 { // genesis seed + our patch
		t.Errorf("own unkeyed cursor = %d, want 2", rec.Cursors["chalkboard"])
	}
	if rec.Cursors["study:@r1"] != 7 || rec.Cursors["study:@r2"] != 42 {
		t.Errorf("extra cursors = %v", rec.Cursors)
	}
	// Plain Append still stamps only the node's own channels.
	_, lt2, err := f.Append(trunk, 0, []byte(`"turn2"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := x.ReadAt(x.main, lt2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec2.Cursors["study:@r1"]; ok {
		t.Errorf("plain append leaked extra cursors: %v", rec2.Cursors)
	}
}
