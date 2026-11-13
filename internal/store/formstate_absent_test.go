package store

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func storeListing(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = info.Size() ^ info.ModTime().UnixNano()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// El Inspector XV: doctor residents asks FormState about ids the store does
// not hold. For a removed id and a made-up one it must fail and leave the
// disk as it was: no channel opened, no sidecar written, no Form kept to
// reopen on the next ask. Its only trace is the in-memory touched entry,
// which EvictIdle clears.
func TestFormStateOnAnIDTheStoreDoesNotHoldChangesNothing(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = be.Close() })
	gone, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"x"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Remove(gone, false); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	_, cached := be.forms[gone]
	delete(be.touched, gone)
	be.mu.Unlock()
	if cached {
		t.Fatal("control: the remove left the Form cached")
	}

	for _, id := range []string{gone, "@0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"} {
		before := storeListing(t, root)
		for range 2 {
			if _, err := be.FormState(id); err == nil {
				t.Fatalf("FormState(%s) answered for an id the store does not hold", id)
			}
		}
		after := storeListing(t, root)
		for path := range after {
			if _, ok := before[path]; !ok {
				t.Errorf("FormState(%s) created %s", id, path)
			}
		}
		for path, stamp := range before {
			if after[path] != stamp {
				t.Errorf("FormState(%s) changed or removed %s", id, path)
			}
		}
		be.mu.Lock()
		_, kept := be.forms[id]
		_, touched := be.touched[id]
		be.mu.Unlock()
		if kept {
			t.Errorf("FormState(%s) kept a Form", id)
		}
		if !touched {
			t.Errorf("FormState(%s): the touched entry this test documents is gone; update the comment", id)
		}
	}
}
