package xwal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

type lazyVariant struct {
	name      string
	channel   string
	reducible bool
}

var lazyVariants = []lazyVariant{
	{name: "keyed-log", channel: "notes"},
	{name: "unkeyed-log", channel: "turnlog"},
	{name: "keyed-reducible", channel: "chalkboard", reducible: true},
	{name: "unkeyed-reducible", channel: "board", reducible: true},
}

func lazyStoreOptions() StoreOptions {
	return StoreOptions{
		Main:         "ir",
		SyncInterval: 20 * time.Millisecond,
		Reducers:     map[string]Reducer{"chalkboard": MapReducer(), "board": MapReducer()},
		Unkeyed:      []string{"turnlog", "board"},
	}
}

func openLazyStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir, lazyStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

// openAfterUpgrade spawns trunks with turns in a store that declares only
// main, then reopens it declaring the variant channels, as an upgrade that
// adds a channel does. Those trunks' nodes predate every variant channel.
func openAfterUpgrade(t *testing.T, turns ...int) (*Store, string, []string) {
	t.Helper()
	dir := t.TempDir()
	early, err := OpenStore(dir, StoreOptions{Main: "ir", SyncInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var trunks []string
	for _, n := range turns {
		trunks = append(trunks, spawnTurns(t, early, n))
	}
	if err := early.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, lazyStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir, trunks
}

func spawnTurns(t *testing.T, s *Store, n int) string {
	t.Helper()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		appendTurn(t, s, string(tr))
	}
	return string(tr)
}

func appendTurn(t *testing.T, s *Store, trunk string) uint64 {
	t.Helper()
	lt, err := s.Append(trunk, "ir", 0, []byte(`{"turn":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return lt
}

func mainTailOf(t *testing.T, s *Store, trunk string) uint64 {
	t.Helper()
	infos, err := s.Channels(trunk)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range infos {
		if c.Name == "ir" {
			return c.Last
		}
	}
	t.Fatal("no main channel")
	return 0
}

func (v lazyVariant) write(t *testing.T, s *Store, trunk, word string) {
	t.Helper()
	payload := []byte(`{"w":"` + word + `"}`)
	if v.reducible {
		var err error
		payload, err = MapSetPatch([]string{word}, json.RawMessage(`true`))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append(trunk, v.channel, mainTailOf(t, s, trunk), payload, nil); err != nil {
		t.Fatalf("%s writes %s on %s: %v", word, v.channel, trunk, err)
	}
}

func (v lazyVariant) words(t *testing.T, s *Store, trunk string) []string {
	t.Helper()
	var last uint64
	infos, err := s.Channels(trunk)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range infos {
		if c.Name == v.channel {
			last = c.Last
		}
	}
	out := []string{}
	if v.reducible {
		state, err := s.StateAt(trunk, v.channel, last)
		if err != nil {
			t.Fatalf("state of %s on %s: %v", v.channel, trunk, err)
		}
		var m map[string]any
		if err := json.Unmarshal(state, &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for lt := uint64(1); lt <= last; lt++ {
		rec, err := s.ReadAt(trunk, v.channel, lt)
		if err != nil {
			t.Fatalf("read %s@%d on %s: %v", v.channel, lt, trunk, err)
		}
		var p struct{ W string }
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.W)
	}
	return out
}

func (v lazyVariant) expect(t *testing.T, s *Store, trunk string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if v.reducible {
		want = append([]string{}, want...)
		sort.Strings(want)
	}
	if got := v.words(t, s, trunk); !reflect.DeepEqual(got, want) {
		t.Errorf("%s on %s reads %v, want %v", v.channel, trunk, got, want)
	}
}

func nodeDir(t *testing.T, s *Store, root, channel, trunk string) string {
	t.Helper()
	node, ok := s.HeadNode(trunk)
	if !ok {
		t.Fatalf("no head node for %s", trunk)
	}
	return filepath.Join(root, channel, fsName(node))
}

// The parent predates the channel and never wrote it: the fork inherits
// nothing there, and each side's first write starts its own log.
func TestLazyChannel_ForkOfANodeThatNeverWrote(t *testing.T) {
	for _, v := range lazyVariants {
		t.Run(v.name, func(t *testing.T) {
			s, root, early := openAfterUpgrade(t, 1, 2)
			other, p := early[0], early[1]
			v.write(t, s, other, "other")
			k, err := s.Fork(p, mainTailOf(t, s, p))
			if err != nil {
				t.Fatal(err)
			}
			v.expect(t, s, k)
			for _, tr := range []string{p, k} {
				if pathExists(nodeDir(t, s, root, v.channel, tr)) {
					t.Fatalf("%s has a %s directory before writing it", tr, v.channel)
				}
			}
			appendTurn(t, s, p)
			appendTurn(t, s, k)
			v.write(t, s, p, "p")
			v.write(t, s, k, "k")
			v.expect(t, s, p, "p")
			v.expect(t, s, k, "k")
			v.expect(t, s, other, "other")
		})
	}
}

// The parent wrote before the fork: the child is given a directory at fork
// time, sees the parent's prefix, and its own writes stay its own.
func TestLazyChannel_ForkAfterTheParentWrote(t *testing.T) {
	for _, v := range lazyVariants {
		t.Run(v.name, func(t *testing.T) {
			s, root := openLazyStore(t)
			p := spawnTurns(t, s, 1)
			v.write(t, s, p, "p0")
			appendTurn(t, s, p)
			k, err := s.Fork(p, mainTailOf(t, s, p))
			if err != nil {
				t.Fatal(err)
			}
			if !pathExists(nodeDir(t, s, root, v.channel, k)) {
				t.Fatalf("fork of a writer has no %s directory", v.channel)
			}
			appendTurn(t, s, p)
			appendTurn(t, s, k)
			v.write(t, s, p, "p1")
			v.write(t, s, k, "k1")
			v.expect(t, s, p, "p0", "p1")
			v.expect(t, s, k, "p0", "k1")
		})
	}
}

// An interior fork of a node with no directory in the channel.
func TestLazyChannel_InteriorForkOfANodeThatNeverWrote(t *testing.T) {
	for _, v := range lazyVariants {
		t.Run(v.name, func(t *testing.T) {
			s, _, early := openAfterUpgrade(t, 1, 3)
			other, p := early[0], early[1]
			v.write(t, s, other, "other")
			k, err := s.Fork(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			appendTurn(t, s, k)
			v.write(t, s, k, "k")
			appendTurn(t, s, p)
			v.write(t, s, p, "p")
			v.expect(t, s, k, "k")
			v.expect(t, s, p, "p")
		})
	}
}

// The grandparent writes only after the child forked, so neither the child
// nor the child's fork may see it, whatever the grandparent's directory
// holds by the time they read.
func TestLazyChannel_GrandparentWritesAfterTheFork(t *testing.T) {
	for _, v := range lazyVariants {
		t.Run(v.name, func(t *testing.T) {
			s, root, early := openAfterUpgrade(t, 1, 2)
			other, g := early[0], early[1]
			v.write(t, s, other, "other")
			k, err := s.Fork(g, mainTailOf(t, s, g))
			if err != nil {
				t.Fatal(err)
			}
			appendTurn(t, s, g)
			v.write(t, s, g, "g")
			appendTurn(t, s, k)
			j, err := s.Fork(k, mainTailOf(t, s, k))
			if err != nil {
				t.Fatal(err)
			}
			if pathExists(nodeDir(t, s, root, v.channel, j)) {
				t.Fatalf("fork of a node without %s got a directory", v.channel)
			}
			appendTurn(t, s, j)
			v.write(t, s, j, "j")
			v.expect(t, s, j, "j")
			v.expect(t, s, k)
			v.expect(t, s, g, "g")
			if pathExists(nodeDir(t, s, root, v.channel, k)) {
				t.Fatalf("reading %s created a directory for a node that never wrote it", v.channel)
			}
		})
	}
}

// Reopening the store must read every case the same way: nothing above
// depends on a handle that outlived the write.
func TestLazyChannel_ReadsSurviveReopen(t *testing.T) {
	for _, v := range lazyVariants {
		t.Run(v.name, func(t *testing.T) {
			s, dir, early := openAfterUpgrade(t, 1, 2)
			other, p := early[0], early[1]
			v.write(t, s, other, "other")
			k, err := s.Fork(p, mainTailOf(t, s, p))
			if err != nil {
				t.Fatal(err)
			}
			appendTurn(t, s, p)
			v.write(t, s, p, "p")
			appendTurn(t, s, k)
			j, err := s.Fork(k, mainTailOf(t, s, k))
			if err != nil {
				t.Fatal(err)
			}
			v.write(t, s, k, "k")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s2, err := OpenStore(dir, lazyStoreOptions())
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			v.expect(t, s2, p, "p")
			v.expect(t, s2, k, "k")
			v.expect(t, s2, j)
			v.expect(t, s2, other, "other")
		})
	}
}

// A new channel's first write is the writer's alone: it creates one node
// directory, and the store's other nodes get nothing.
func TestLazyChannel_FirstWriteTouchesOnlyTheWriter(t *testing.T) {
	s, root := openLazyStore(t)
	var trunks []string
	for i := 0; i < 20; i++ {
		trunks = append(trunks, spawnTurns(t, s, 2))
	}
	if _, err := s.Append(trunks[7], "fresh", mainTailOf(t, s, trunks[7]), []byte(`{"w":"x"}`), nil); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(root, "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	want := filepath.Base(nodeDir(t, s, root, "fresh", trunks[7]))
	if len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("channel dirs after the first write = %v, want only %s", dirs, want)
	}
}

// A new channel's first write neither waits on another lineage's borrowed
// head nor disturbs it: before lazy channel nodes it took the store-wide
// topology mutation and stalled every turn in the store behind a backfill.
func TestLazyChannel_FirstWriteIgnoresAnotherLineagesHead(t *testing.T) {
	shortTopologyWait(t)
	s, _ := openLazyStore(t)
	a := spawnTurns(t, s, 1)
	b := spawnTurns(t, s, 1)
	head, err := s.Trunks.Head(b)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	s.Trunks.hotMu.Lock()
	before := s.Trunks.hot
	s.Trunks.hotMu.Unlock()
	if _, err := s.Append(a, "fresh", 1, []byte(`{"w":"a"}`), nil); err != nil {
		t.Fatalf("first write on a new channel with a peer head borrowed: %v", err)
	}
	s.Trunks.hotMu.Lock()
	after := s.Trunks.hot
	s.Trunks.hotMu.Unlock()
	if after != before {
		t.Fatal("a new channel's first write retired the hot generation")
	}
	if _, err := head.Append("fresh", 1, []byte(`{"w":"b"}`), nil); err != nil {
		t.Fatalf("the borrowed head writes the new channel: %v", err)
	}
	if _, err := s.Append(b, "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
}
