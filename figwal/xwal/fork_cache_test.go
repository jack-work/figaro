package xwal

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func forkCacheStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1, NoBackgroundFlush: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Trunks.ensureChannel(ChannelSpec{Name: "aux", Kind: ChannelLog}); err != nil {
		t.Fatal(err)
	}
	return s
}

func auxRecords(t *testing.T, s *Store, trunk string) []string {
	t.Helper()
	rs, err := s.RecordsFrom(trunk, "aux", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, string(r.Payload))
	}
	return out
}

// A fork's channel .fork files are written unsynced; its .node, synced, holds
// every base. Power lost before they reach the disk can leave a .fork
// missing, empty, or its whole directory gone. Read as they are, the child
// would own its channels from 1 and lose what it inherited; the open after
// the crash must rebuild them from .node instead.
func TestAForkMarkerLostInACrashIsRebuiltFromTheNode(t *testing.T) {
	for _, loss := range []string{"missing", "empty", "dir gone", "missing after a reparent"} {
		t.Run(loss, func(t *testing.T) {
			dir := t.TempDir()
			s := forkCacheStore(t, dir)
			a, _ := s.SpawnUnderRoot()
			for range 3 {
				lt, err := s.Append(a, "main", 0, []byte(`"a"`), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Append(a, "aux", lt, []byte(`"aux-a"`), nil); err != nil {
					t.Fatal(err)
				}
			}
			s.syncDirty()
			c, err := s.ForkTail(a)
			if err != nil {
				t.Fatal(err)
			}
			if loss == "missing after a reparent" {
				if c, err = s.ForkTail(c); err != nil {
					t.Fatal(err)
				}
				if err := s.Reparent(s.head(c), s.head(a)); err != nil {
					t.Fatal(err)
				}
			}
			key := s.head(c)
			want := auxRecords(t, s, c)
			if len(want) != 3 {
				t.Fatalf("the fork reads %v from aux, want its parent's three records", want)
			}
			mainDir, auxDir := filepath.Join(dir, "main", fsName(key)), filepath.Join(dir, "aux", fsName(key))
			wantBase, err := readForkBaseFile(mainDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Abandon(); err != nil {
				t.Fatal(err)
			}

			switch loss {
			case "missing", "missing after a reparent":
				for _, d := range []string{mainDir, auxDir} {
					if err := os.Remove(filepath.Join(d, ".fork")); err != nil {
						t.Fatal(err)
					}
				}
			case "empty":
				for _, d := range []string{mainDir, auxDir} {
					if err := os.Truncate(filepath.Join(d, ".fork"), 0); err != nil {
						t.Fatal(err)
					}
				}
			case "dir gone":
				if err := os.RemoveAll(auxDir); err != nil {
					t.Fatal(err)
				}
			}

			s = forkCacheStore(t, dir)
			defer s.Close()
			if got := auxRecords(t, s, c); !slices.Equal(got, want) {
				t.Fatalf("after the crash the fork reads %v from aux, want %v", got, want)
			}
			if got, err := readForkBaseFile(mainDir); err != nil || got != wantBase {
				t.Fatalf("main .fork after repair: %d, %v; want %d", got, err, wantBase)
			}
		})
	}
}
