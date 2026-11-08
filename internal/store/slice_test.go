package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// The snapshot dev shell copies a slice of the real store: the most recently
// active arias, everything their history reads, and every form. These pin
// what the slice keeps and that it opens as a store whose forks still read
// their parents' history.

type sliceFixture struct {
	t    *testing.T
	root string
	b    *store.XwalBackend
}

func newSliceFixture(t *testing.T) *sliceFixture {
	t.Helper()
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	return &sliceFixture{t: t, root: root, b: b}
}

func (f *sliceFixture) aria(turns int, text string) string {
	f.t.Helper()
	outfit, err := f.b.CreateOutfit("slice", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := f.b.CreateConversation(outfit)
	if err != nil {
		f.t.Fatal(err)
	}
	f.say(id, turns, text)
	return id
}

func (f *sliceFixture) say(id string, turns int, text string) {
	f.t.Helper()
	ir, err := f.b.OpenFigIR(id)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i < turns; i++ {
		msg := message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}
		if _, err := ir.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *sliceFixture) node(trunk string) string {
	f.t.Helper()
	l, err := xwal.ReadLayout(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	for key, n := range l.Nodes {
		if n.Trunk == trunk {
			return key
		}
	}
	f.t.Fatalf("no node holds %s", trunk)
	return ""
}

// activeAt backdates every file the aria's node owns, which is all the slice
// reads to rank it.
func (f *sliceFixture) activeAt(trunk string, at time.Time) {
	f.t.Helper()
	l, err := xwal.ReadLayout(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	dir := xwal.NodeDirName(f.node(trunk))
	paths := []string{filepath.Join(f.root, "_meta", trunk+".json")}
	for _, ch := range l.Channels {
		entries, _ := os.ReadDir(filepath.Join(f.root, ch, dir))
		for _, e := range entries {
			paths = append(paths, filepath.Join(f.root, ch, dir, e.Name()))
		}
	}
	for _, p := range paths {
		if err := os.Chtimes(p, at, at); err != nil && !os.IsNotExist(err) {
			f.t.Fatal(err)
		}
	}
}

func (f *sliceFixture) slice(opt store.SliceOptions) (string, store.SliceReport) {
	f.t.Helper()
	dst := filepath.Join(f.t.TempDir(), "arias")
	rep, err := store.CopySlice(f.root, dst, opt)
	if err != nil {
		f.t.Fatal(err)
	}
	return dst, rep
}

func openSlice(t *testing.T, dst string) *store.XwalBackend {
	t.Helper()
	b, err := store.NewXwalBackend(dst, 0)
	if err != nil {
		t.Fatalf("the slice does not open as a store: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestASliceKeepsTheNewestAriasAndTheHistoryTheirForksRead(t *testing.T) {
	f := newSliceFixture(t)
	parent := f.aria(6, "parent")
	_, fork, err := f.b.ForkAt(parent, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.say(fork, 2, "fork")
	recent := f.aria(1, "recent")
	stale := f.aria(1, "stale")
	formID, _, err := f.b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"name": json.RawMessage(`"kept"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	history := func(b *store.XwalBackend, id string) string {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			t.Fatalf("%s does not open: %v", id, err)
		}
		var turns []any
		for _, e := range ir.Read() {
			turns = append(turns, []any{e.LT, e.Payload})
		}
		raw, err := json.Marshal(turns)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	forkHistory := history(f.b, fork)
	if !strings.Contains(forkHistory, "parent") {
		t.Fatalf("the fixture's fork does not read its parent's turns: %s", forkHistory)
	}

	now := time.Now()
	f.activeAt(fork, now)
	f.activeAt(recent, now.Add(-time.Hour))
	f.activeAt(parent, now.Add(-48*time.Hour))
	f.activeAt(stale, now.Add(-72*time.Hour))

	dst, rep := f.slice(store.SliceOptions{Arias: 2})
	if rep.Arias != 2 || rep.Skipped != 0 {
		t.Fatalf("report %+v, want 2 arias, none passed over", rep)
	}
	b := openSlice(t, dst)
	have := ttlIDs(b)
	for _, id := range []string{fork, recent, formID} {
		if !have[id] {
			t.Errorf("the slice lost %s", id)
		}
	}
	if have[stale] {
		t.Errorf("the slice kept %s, the least recently active aria", stale)
	}
	if got := history(b, fork); got != forkHistory {
		t.Fatalf("the fork reads another history in the slice: its parent's did not come along\n  slice: %s\n  store: %s", got, forkHistory)
	}
	if _, err := os.Stat(filepath.Join(dst, "_meta", stale+".json")); err == nil {
		t.Errorf("the slice kept the sidecar of %s, which it left behind", stale)
	}
}

func TestASliceUnderACapPassesOverAnAriaTooBigForIt(t *testing.T) {
	f := newSliceFixture(t)
	big := f.aria(400, strings.Repeat("x", 4096))
	small := f.aria(1, "small")
	now := time.Now()
	f.activeAt(big, now)
	f.activeAt(small, now.Add(-time.Hour))

	dst, rep := f.slice(store.SliceOptions{Arias: 2, MaxBytes: 1 << 20})
	if rep.Arias != 1 || rep.Skipped != 1 || rep.Bytes > 1<<20 {
		t.Fatalf("report %+v, want the small aria alone under 1 MiB, the big one passed over", rep)
	}
	have := ttlIDs(openSlice(t, dst))
	if have[big] || !have[small] {
		t.Fatalf("the slice holds big=%v small=%v, want only the small one", have[big], have[small])
	}
}

func TestASliceOfNoCountKeepsEveryAria(t *testing.T) {
	f := newSliceFixture(t)
	ids := []string{f.aria(1, "a"), f.aria(1, "b"), f.aria(1, "c")}
	dst, rep := f.slice(store.SliceOptions{})
	if rep.Arias != len(ids) {
		t.Fatalf("report %+v, want all %d arias", rep, len(ids))
	}
	have := ttlIDs(openSlice(t, dst))
	for _, id := range ids {
		if !have[id] {
			t.Errorf("the slice lost %s", id)
		}
	}
}

func TestASliceRefusesAnExistingDestination(t *testing.T) {
	f := newSliceFixture(t)
	if _, err := store.CopySlice(f.root, t.TempDir(), store.SliceOptions{}); err == nil {
		t.Fatal("the slice wrote into a directory that already existed")
	}
}
