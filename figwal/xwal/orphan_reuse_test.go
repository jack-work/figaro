package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

// A fork that crashes before its .node commits leaves directories the index
// does not count. Minting that name again handed the new node the orphan's
// fork markers (issue #173).
func TestACrashedForksNameIsNeverReissued(t *testing.T) {
	dir := t.TempDir()
	open := func() *Store {
		s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Trunks.ensureChannel(ChannelSpec{Name: "aux", Kind: ChannelLog}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	a, _ := s.SpawnUnderRoot()
	for i := range 3 {
		lt, err := s.Append(a, "main", 0, []byte(`"a"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(a, "aux", lt, []byte(`"aux-a"`), nil); err != nil {
			t.Fatal(err, i)
		}
	}
	b, _ := s.SpawnUnderRoot()
	if _, err := s.Append(b, "main", 0, []byte(`"b"`), nil); err != nil {
		t.Fatal(err)
	}
	s.syncDirty()
	c, err := s.ForkTail(a)
	if err != nil {
		t.Fatal(err)
	}
	ckey := s.head(c)
	if !pathExists(filepath.Join(dir, "aux", fsName(ckey), ".fork")) {
		t.Fatalf("the crashed fork %s wrote no aux fork marker; the test proves nothing", ckey)
	}
	s.syncDirty()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "main", fsName(ckey), ".node")); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	d, err := s.ForkTail(b)
	if err != nil {
		t.Fatal(err)
	}
	dkey := s.head(d)
	body, _ := os.ReadFile(filepath.Join(dir, "aux", fsName(dkey), ".fork"))
	if dkey == ckey {
		t.Fatalf("node %s was reissued over a crashed fork (its aux fork marker reads %q)", dkey, body)
	}
	if len(body) > 0 {
		t.Fatalf("the new node %s has an aux fork marker %q its parent never had", dkey, body)
	}
}
