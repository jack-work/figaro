package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

// A record synced through is on disk before anything publishes it, which is
// the whole claim sync-before-publish makes. Reading it back from a second
// handle proves the bytes left the process, not merely the buffer.
func TestSyncChannelThroughPersists(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, StoreOptions{Main: "main", NoBackgroundFlush: true})
	if err != nil {
		t.Fatal(err)
	}
	trunk, err := st.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	_, lt, _, err := st.AppendCursors(trunk, []byte(`{"a":1}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncChannelThrough(trunk, "main", lt); err != nil {
		t.Fatalf("sync through: %v", err)
	}
	// The segment file must hold the record without any close or flush.
	found := false
	_ = filepath.Walk(filepath.Join(dir, "main"), func(p string, fi os.FileInfo, e error) error {
		if e != nil || fi.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && len(b) > 0 && contains(b, `{"a":1}`) {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("record not on disk after SyncChannelThrough")
	}
	st.Close()
}

func contains(b []byte, s string) bool {
	return len(b) >= len(s) && stringIndex(string(b), s) >= 0
}

func stringIndex(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
