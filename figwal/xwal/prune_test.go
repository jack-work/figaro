package xwal

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type pruneFixture struct {
	s       *Store
	root    string
	notes   lazyVariant
	trunks  map[string]string
	dirs    map[string]string
	before  map[string][]string
	segName string
}

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// One node per shape in the notes channel, the rest made the way older
// releases left them: by hand, since forks no longer write them.
func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	s, root := openLazyStore(t)
	f := &pruneFixture{s: s, root: root, notes: lazyVariants[0], trunks: map[string]string{}, dirs: map[string]string{}}
	for _, name := range []string{"rows", "zero", "base1", "inherits", "parent", "empty", "zerobase1"} {
		f.trunks[name] = spawnTurns(t, s, 2)
	}
	f.notes.write(t, s, f.trunks["rows"], "kept")
	s.syncDirty()
	for name, tr := range f.trunks {
		f.dirs[name] = nodeDir(t, s, root, f.notes.channel, tr)
	}
	ents, err := os.ReadDir(f.dirs["rows"])
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepathIsSegment(e.Name()) {
			f.segName = e.Name()
		}
	}
	if f.segName == "" {
		t.Fatal("setup: the writer has no segment")
	}
	writeFile(t, filepath.Join(f.dirs["zero"], f.segName), nil)
	writeFile(t, filepath.Join(f.dirs["base1"], ".fork"), []byte("base=1\n"))
	writeFile(t, filepath.Join(f.dirs["inherits"], ".fork"), []byte("base=3\n"))
	writeFile(t, filepath.Join(f.dirs["zerobase1"], ".fork"), []byte("base=1\n"))
	writeFile(t, filepath.Join(f.dirs["zerobase1"], f.segName), nil)
	if err := os.MkdirAll(f.dirs["empty"], 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.dirs["parent"], f.segName), nil)
	child, err := s.Fork(f.trunks["parent"], mainTailOf(t, s, f.trunks["parent"]))
	if err != nil {
		t.Fatal(err)
	}
	f.trunks["child"] = child
	f.dirs["child"] = nodeDir(t, s, root, f.notes.channel, child)
	writeFile(t, filepath.Join(f.dirs["child"], ".fork"), []byte("base=2\n"))
	f.dirs["stray"] = filepath.Join(root, f.notes.channel, "not-a-node")
	writeFile(t, filepath.Join(f.dirs["stray"], f.segName), nil)

	f.before = map[string][]string{}
	for name, tr := range f.trunks {
		f.before[name] = f.view(t, tr)
	}
	return f
}

func (f *pruneFixture) view(t *testing.T, trunk string) []string {
	t.Helper()
	infos, err := f.s.Channels(trunk)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for _, c := range infos {
		if c.Name == f.notes.channel {
			last = c.Last
		}
	}
	out := []string{}
	for lt := uint64(1); lt <= last; lt++ {
		rec, err := f.s.ReadAt(trunk, f.notes.channel, lt)
		if err != nil {
			out = append(out, "error: "+err.Error())
			continue
		}
		out = append(out, string(rec.Payload))
	}
	return out
}

func filepathIsSegment(name string) bool {
	return filepath.Ext(name) == ".seg" || filepath.Ext(name) == ".jsonl"
}

func countsOf(rep ChannelDirPrune, channel string) map[DirShape][2]int {
	out := map[DirShape][2]int{}
	for _, c := range rep.Counts {
		if c.Channel == channel {
			out[c.Shape] = [2]int{c.Removed, c.Kept}
		}
	}
	return out
}

func TestPruneChannelDirs_RemovesOnlyDirsThatHoldNoRowsAndPassNothingOn(t *testing.T) {
	f := newPruneFixture(t)
	want := map[DirShape][2]int{
		ShapeZeroByteOnly:  {1, 0},
		ShapeForkBaseOne:   {2, 0},
		ShapeEmpty:         {1, 0},
		ShapeInheritsNoRow: {0, 2},
		ShapeRows:          {0, 1},
		ShapeChildInherits: {0, 1},
		ShapeUnindexed:     {0, 1},
	}
	dry, err := f.s.PruneEmptyChannelDirs(true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Skipped != "" {
		t.Fatalf("skipped: %s", dry.Skipped)
	}
	if got := countsOf(dry, f.notes.channel); !reflect.DeepEqual(got, want) {
		t.Fatalf("dry run counts\n%v\nwant\n%v", got, want)
	}
	for name, dir := range f.dirs {
		if !pathExists(dir) {
			t.Fatalf("the dry run removed %s", name)
		}
	}

	real, err := f.s.PruneEmptyChannelDirs(false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(real.Counts, dry.Counts) {
		t.Fatalf("the real run's list differs from the dry run's\n%v\n%v", real.Counts, dry.Counts)
	}
	gone := map[string]bool{"zero": true, "base1": true, "zerobase1": true, "empty": true}
	for name, dir := range f.dirs {
		if pathExists(dir) == gone[name] {
			t.Errorf("%s: exists=%v after the prune, want %v", name, pathExists(dir), !gone[name])
		}
	}
	for name, tr := range f.trunks {
		if got := f.view(t, tr); !reflect.DeepEqual(got, f.before[name]) {
			t.Errorf("%s reads %v after the prune, %v before", name, got, f.before[name])
		}
	}
	appendTurn(t, f.s, f.trunks["zero"])
	f.notes.write(t, f.s, f.trunks["zero"], "after")
	f.notes.expect(t, f.s, f.trunks["zero"], "after")
}

// Canary target: a prune that ignores the bytes in a segment removes the
// writer's directory and this goes red.
func TestPruneChannelDirs_ADirWithAnyByteStays(t *testing.T) {
	f := newPruneFixture(t)
	writeFile(t, filepath.Join(f.dirs["base1"], f.segName), []byte{0})
	if _, err := f.s.PruneEmptyChannelDirs(false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rows", "base1"} {
		if !pathExists(f.dirs[name]) {
			t.Fatalf("%s held a byte and was removed", name)
		}
	}
	f.notes.expect(t, f.s, f.trunks["rows"], "kept")
}

func TestPruneChannelDirs_TheMainChannelIsNeverTouched(t *testing.T) {
	f := newPruneFixture(t)
	rep, err := f.s.PruneEmptyChannelDirs(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := countsOf(rep, "ir"); len(got) != 0 {
		t.Fatalf("the main channel was walked: %v", got)
	}
	for _, tr := range f.trunks {
		if !pathExists(filepath.Join(nodeDir(t, f.s, f.root, "ir", tr), nodeMarkerName)) {
			t.Fatalf("%s lost its node marker", tr)
		}
	}
}
