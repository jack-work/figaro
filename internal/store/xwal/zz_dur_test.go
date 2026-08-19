package xwal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dirBytes(t *testing.T, dir string) int64 {
	var n int64
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			n += fi.Size()
		}
	}
	return n
}

func TestDbgChalkDurability(t *testing.T) {
	for _, unkeyed := range []bool{false, true} {
		dir := t.TempDir()
		opts := testStoreOptions()
		opts.SyncInterval = 20 * time.Millisecond
		if unkeyed {
			opts.Unkeyed = []string{"chalkboard"}
		}
		s, err := OpenStore(dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := s.SpawnUnderRoot()
		for i := 0; i < 3; i++ {
			s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil)
		}
		time.Sleep(150 * time.Millisecond)
		node, _ := s.HeadNode(string(tr))
		cbDir := filepath.Join(dir, "chalkboard", node)
		before := dirBytes(t, cbDir)
		if _, err := s.Append(string(tr), "chalkboard", 0, []byte(`{"k":"v"}`), nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		after := dirBytes(t, cbDir)
		s.Kick()
		time.Sleep(200 * time.Millisecond)
		afterKick := dirBytes(t, cbDir)
		s.Close()
		afterClose := dirBytes(t, cbDir)
		t.Logf("unkeyed=%v  bytes before=%d afterFlushWindow=%d afterKick=%d afterClose=%d",
			unkeyed, before, after, afterKick, afterClose)
	}
}
