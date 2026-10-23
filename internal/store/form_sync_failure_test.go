package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type flakyFormLog struct {
	records  [][]byte
	failSync int
}

func (l *flakyFormLog) AppendPatch(payload []byte) (uint64, error) {
	l.records = append(l.records, append([]byte(nil), payload...))
	return uint64(len(l.records)), nil
}

func (l *flakyFormLog) SyncThrough(uint64) error {
	if l.failSync > 0 {
		l.failSync--
		return errors.New("sync: input/output error")
	}
	return nil
}

func (l *flakyFormLog) DiscardFrom(index uint64) error {
	if index >= 1 && int(index) <= len(l.records) {
		l.records = l.records[:index-1]
	}
	return nil
}

func (l *flakyFormLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	if from < 1 {
		from = 1
	}
	for i := from; int(i) <= len(l.records); i++ {
		if upTo > 0 && i > upTo {
			break
		}
		if err := fn(i, l.records[i-1]); err != nil {
			return err
		}
	}
	return nil
}

// A write refused because its sync failed must not come back: the record it
// appended stays in the log otherwise, a restart replays it, and the caller
// that was told "failed" finds its value in place.
func TestAWriteWhoseSyncFailedDoesNotResurrect(t *testing.T) {
	log := &flakyFormLog{}
	f, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	set := func(k, v string) error {
		raw, _ := json.Marshal(v)
		_, err := f.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{k: raw}, nil), 0)
		return err
	}
	if err := set("kept", "1"); err != nil {
		t.Fatal(err)
	}
	log.failSync = 1
	if err := set("refused", "x"); err == nil {
		t.Fatal("a write whose sync failed was reported as done")
	}
	if err := set("after", "2"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again, err := OpenForm(log)
	if err != nil {
		t.Fatal(err)
	}
	snap, version := again.Snapshot()
	if snap.Has("refused") {
		t.Fatal("the refused write came back on reopen")
	}
	if !snap.Has("kept") || !snap.Has("after") {
		t.Fatalf("reopened state lost an accepted write: %v", snap)
	}
	if live, _ := f.Snapshot(); version != f.Read().Version || live.Has("refused") {
		t.Fatalf("reopened at version %d, the live form stood at %d", version, f.Read().Version)
	}
}

// The same promise on a real store: a bound-form write refused because its
// channel could not sync is gone after the backend reopens, and the writes
// either side of it survive.
func TestARefusedBoundFormWriteIsGoneAfterReopen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	root := t.TempDir()
	b, err := NewXwalBackend(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	id := mustNewAria(t, b)
	setBoundFormKey(t, b, id, "kept", "1")

	node, ok := b.store.trunks.HeadNode(id)
	if !ok {
		t.Fatal("no head node")
	}
	var dir string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && filepath.Base(filepath.Dir(p)) == chanForm && d.Name() == node {
			dir = p
		}
		return nil
	})
	if dir == "" {
		t.Fatalf("no form channel dir for node %s", node)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	big, _ := json.Marshal(strings.Repeat("x", 4096))
	if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"refused": big}, nil)); err == nil {
		t.Skip("the write synced into the open segment; this filesystem gave no failure to test")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setBoundFormKey(t, b, id, "after", "2")
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := NewXwalBackend(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	snap, err := again.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Has("refused") {
		t.Fatal("a write refused for want of a sync came back after reopen")
	}
	if !snap.Has("kept") || !snap.Has("after") {
		t.Fatalf("an accepted write was lost: %v", snap)
	}
}
