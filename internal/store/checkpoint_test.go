package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func checkpointEvery(t testing.TB, patches, maxMarks int) {
	t.Helper()
	p, s, m := CheckpointDefaultsForTest()
	SetCheckpointDefaults(patches, 0, maxMarks)
	t.Cleanup(func() { SetCheckpointDefaults(p, s, m) })
}

func settleCheckpoints(t testing.TB, b *XwalBackend, id string) {
	t.Helper()
	f, err := b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	f.ckptGate.Lock()
	f.ckptGate.Unlock()
	f.ckpt.Wait()
}

// A cold open starts from the newest checkpoint, not the segment header: the
// state and version are the replay's, and the history under it still reads.
func TestAColdOpenStartsFromTheNewestCheckpoint(t *testing.T) {
	checkpointEvery(t, 64, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 300, 40)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	want, wantVersion := f.Snapshot()
	marks := MarksOf(want)
	if len(marks) == 0 {
		t.Fatal("300 patches at every 64 left no mark")
	}
	b.dropForm(id)

	f, err = b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	got, gotVersion := f.Snapshot()
	if gotVersion != wantVersion || snapJSON(t, got) != snapJSON(t, want) {
		t.Fatalf("cold open from a checkpoint: version %d state %d bytes, want %d / %d bytes",
			gotVersion, len(snapJSON(t, got)), wantVersion, len(snapJSON(t, want)))
	}
	if base := f.Base(); base.Version < wantVersion-64-1 {
		t.Fatalf("opened from version %d, the newest checkpoint is within 64 of %d", base.Version, wantVersion)
	}
	ps, err := f.PatchesBetween(0, wantVersion)
	if err != nil || len(ps) < 300 {
		t.Fatalf("history under the checkpoint: %d patches, %v", len(ps), err)
	}
}

// Marks are capped: the least recently used goes, the tail never does, and
// a mark's file goes with it.
func TestMarksAreCappedAndTheirFilesFollow(t *testing.T) {
	checkpointEvery(t, 64, 3)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 64*8, 10)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	snap, head := f.Snapshot()
	marks := MarksOf(snap)
	if len(marks) != 3 {
		t.Fatalf("%d marks, want the cap of 3: %v", len(marks), marks)
	}
	tails := 0
	var newest uint64
	for v, m := range marks {
		if m.Pin == PinTail {
			tails++
			newest = v
		}
	}
	if tails != 1 || head-newest > 64 {
		t.Fatalf("tail marks %d at %d, head %d", tails, newest, head)
	}
	files := checkpointVersions(checkpointDirOf(b.store.root, id))
	if len(files) != len(marks) {
		t.Fatalf("files %v for marks %v", files, marks)
	}
	for _, v := range files {
		if _, ok := marks[v]; !ok {
			t.Fatalf("file %d outlived its mark", v)
		}
	}
}

// A checkpoint whose record was cut and rewritten is a stranger's state; a
// torn one is noise. Either way the open falls back and the state is right.
func TestABadCheckpointIsPassedOver(t *testing.T) {
	checkpointEvery(t, 64, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 200, 10)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	want, wantVersion := f.Snapshot()
	b.dropForm(id)

	dir := checkpointDirOf(b.store.root, id)
	vs := checkpointVersions(dir)
	newest := vs[len(vs)-1]
	path := filepath.Join(dir, checkpointName(newest))
	c, err := readCheckpointFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lie := form.Snapshot{}.SetPath("k0", json.RawMessage(`"lie"`))
	body, _ := encodeFormWatermark(lie, nil)
	if err := writeCheckpointFile(dir, newest, c.RecordCRC+1, body); err != nil {
		t.Fatal(err)
	}
	if len(vs) > 1 {
		older := filepath.Join(dir, checkpointName(vs[len(vs)-2]))
		if err := os.WriteFile(older, []byte(checkpointMagic+"{"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err = b.form(id)
	if err != nil {
		t.Fatal(err)
	}
	got, gotVersion := f.Snapshot()
	if gotVersion != wantVersion || snapJSON(t, got) != snapJSON(t, want) {
		t.Fatalf("a bad checkpoint was believed: version %d", gotVersion)
	}
}

// Checkpoints are off unless asked for: a form that sets nothing under a
// store that sets nothing writes no marks and no files.
func TestNoCheckpointsByDefault(t *testing.T) {
	checkpointEvery(t, 0, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := longBoard(t, b, 300, 10)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	snap, _ := f.Snapshot()
	if MarksOf(snap) != nil || len(checkpointVersions(checkpointDirOf(b.store.root, id))) > 0 {
		t.Fatal("checkpoints written with the policy off")
	}
}

// The form's own key turns it on and its floor holds.
func TestAFormsCheckpointKeyWinsOverTheDefault(t *testing.T) {
	checkpointEvery(t, 0, 5)
	p := checkpointPolicyOf(form.Snapshot{}.SetPath(KeyCheckpointEveryPatches, json.RawMessage(`3`)))
	if p.everyPatches != minCheckpointEveryPatches {
		t.Fatalf("every_patches 3 gave %d, want the floor %d", p.everyPatches, minCheckpointEveryPatches)
	}
	p = checkpointPolicyOf(form.Snapshot{}.SetPath(KeyCheckpointEverySeconds, json.RawMessage(`1`)))
	if p.every != minCheckpointEverySeconds*time.Second {
		t.Fatalf("every_seconds 1 gave %v", p.every)
	}
}

func applyN(t testing.TB, b *XwalBackend, id string, n int, tag string) {
	t.Helper()
	for i := 0; i < n; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("%s-%d", tag, i))
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			fmt.Sprintf("%s.k%d", tag, i%40): raw,
		}, nil)); err != nil {
			t.Fatal(err)
		}
	}
}

// A fork pins a mark at its fork point on the parent, and the state there is
// read from that mark: the same state a replay from record 1 gives.
func TestAForkPinsAMarkAtItsForkPoint(t *testing.T) {
	checkpointEvery(t, 64, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent := mustNewAriaTB(t, b)
	applyN(t, b, parent, 100, "p")
	f, _ := b.form(parent)
	_, at := f.Snapshot()
	want, err := f.StateAt(at)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := b.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"c"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	applyN(t, b, parent, 10, "q")
	settleCheckpoints(t, b, parent)
	snap, _ := f.Snapshot()
	m, ok := MarksOf(snap)[at]
	if !ok || m.Pin != "fork:"+child {
		t.Fatalf("no fork mark at %d: %v", at, MarksOf(snap))
	}
	f.touched.Delete(at)
	got, err := f.StateAt(at)
	if err != nil || snapJSON(t, got) != snapJSON(t, want) {
		t.Fatalf("state at the fork point from its mark differs: %v", err)
	}
	if _, ok := f.touched.Load(at); !ok {
		t.Fatal("the read did not come through the fork's mark")
	}
}

// A read refreshes a mark: of two old marks, the one read survives the
// eviction and the one not read goes.
func TestAReadMarkOutlivesAnUnreadOne(t *testing.T) {
	checkpointEvery(t, 64, 3)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := mustNewAriaTB(t, b)
	applyN(t, b, id, 64*3, "a")
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	snap, _ := f.Snapshot()
	var old []uint64
	for v, m := range MarksOf(snap) {
		if m.Pin != PinTail {
			old = append(old, v)
		}
	}
	if len(old) != 2 {
		t.Fatalf("want two old marks, got %v", MarksOf(snap))
	}
	slices.Sort(old)
	time.Sleep(2 * time.Millisecond)
	if _, err := f.StateAt(old[0] + 1); err != nil {
		t.Fatal(err)
	}
	applyN(t, b, id, 64, "b")
	settleCheckpoints(t, b, id)
	snap, _ = f.Snapshot()
	marks := MarksOf(snap)
	if _, ok := marks[old[0]]; !ok {
		t.Fatalf("the mark read at %d was evicted: %v", old[0], marks)
	}
	if _, ok := marks[old[1]]; ok {
		t.Fatalf("the unread mark %d outlived the read one: %v", old[1], marks)
	}
}
