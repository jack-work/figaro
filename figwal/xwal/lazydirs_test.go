package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

// A node gets a directory in a non-main channel only when it has something
// to inherit there or writes there. These tests defend the readers of a
// channel directory against its absence, which used to be rare (a channel
// added after the node) and is now the common case. Reducible channels
// inherit the root's watermark, so every fork has a base past 1 in them and
// keeps its directory; only the log variants can go without one.

var logVariants = func() []lazyVariant {
	var out []lazyVariant
	for _, v := range lazyVariants {
		if !v.reducible {
			out = append(out, v)
		}
	}
	return out
}()

var variantChannels = func() []string {
	var out []string
	for _, v := range logVariants {
		out = append(out, v.channel)
	}
	return out
}()

func openRegisteredLazyStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, root := openLazyStore(t)
	other := spawnTurns(t, s, 1)
	for _, v := range lazyVariants {
		v.write(t, s, other, "other")
	}
	return s, root
}

func nonMainDirs(t *testing.T, s *Store, root, trunk string) []string {
	t.Helper()
	var found []string
	for _, ch := range variantChannels {
		if dir := nodeDir(t, s, root, ch, trunk); pathExists(dir) {
			found = append(found, dir)
		}
	}
	return found
}

// Canary target: restoring the eager create in writeChannelForks turns this red.
func TestLazyDirs_AForkThatInheritsNothingWritesNoChannelDir(t *testing.T) {
	s, root := openRegisteredLazyStore(t)
	p := spawnTurns(t, s, 3)
	tail, err := s.Fork(p, mainTailOf(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	interior, err := s.Fork(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChildKind(TrunkID(p), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateStump("lazy-stump"); err != nil {
		t.Fatal(err)
	}
	under, err := s.SpawnUnderStump("lazy-stump")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range []string{p, tail, interior, string(child), string(under)} {
		if got := nonMainDirs(t, s, root, tr); len(got) != 0 {
			t.Errorf("%s inherits nothing but has channel dirs %v", tr, got)
		}
		if !pathExists(filepath.Join(nodeDir(t, s, root, "ir", tr), nodeMarkerName)) {
			t.Errorf("%s has no node marker in the main channel", tr)
		}
	}
	for _, tr := range []string{tail, interior, string(child)} {
		if !pathExists(filepath.Join(nodeDir(t, s, root, "ir", tr), ".fork")) {
			t.Errorf("%s lost its main-channel fork base", tr)
		}
	}
	if !pathExists(filepath.Join(root, "ir", fsName("lazy-stump"))) {
		t.Error("the stump has no main-channel directory")
	}
}

func TestLazyDirs_AForkThatInheritsRowsWritesItsBaseBeforeTheNode(t *testing.T) {
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
			base, err := readForkBaseFile(nodeDir(t, s, root, v.channel, k))
			if err != nil {
				t.Fatalf("the inheriting fork has no %s fork base: %v", v.channel, err)
			}
			if base <= 1 {
				t.Fatalf("%s base %d, want past the parent's row", v.channel, base)
			}
		})
	}
}

func TestLazyDirs_ReadsOfAMissingDirAreEmptyAndTheFirstWriteOwnsItsLog(t *testing.T) {
	for _, v := range logVariants {
		t.Run(v.name, func(t *testing.T) {
			s, root := openRegisteredLazyStore(t)
			p := spawnTurns(t, s, 2)
			k, err := s.Fork(p, mainTailOf(t, s, p))
			if err != nil {
				t.Fatal(err)
			}
			v.expect(t, s, k)
			infos, err := s.Channels(k)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range infos {
				if c.Err != nil {
					t.Fatalf("channel %s on a fork with no dir: %v", c.Name, c.Err)
				}
				if c.Name == v.channel && c.Last != 0 {
					t.Fatalf("%s reports last %d with no dir", c.Name, c.Last)
				}
			}
			if _, ok, err := s.Lookup(k, v.channel, mainTailOf(t, s, k)); err != nil || ok {
				t.Fatalf("lookup on a missing dir: ok=%v err=%v", ok, err)
			}
			if recs, err := s.RecordsFrom(k, v.channel, 1, 10); err != nil || len(recs) != 0 {
				t.Fatalf("records from a missing dir: %d, %v", len(recs), err)
			}
			if got := nonMainDirs(t, s, root, k); len(got) != 0 {
				t.Fatalf("reading created %v", got)
			}

			appendTurn(t, s, k)
			v.write(t, s, k, "k")
			base, err := readForkBaseFile(nodeDir(t, s, root, v.channel, k))
			if err != nil || base != 1 {
				t.Fatalf("first write: base %d, err %v; want its own log from 1", base, err)
			}
			appendTurn(t, s, p)
			v.write(t, s, p, "p")
			v.expect(t, s, k, "k")
			v.expect(t, s, p, "p")

			appendTurn(t, s, k)
			g, err := s.Fork(k, mainTailOf(t, s, k))
			if err != nil {
				t.Fatal(err)
			}
			v.expect(t, s, g, "k")
			appendTurn(t, s, g)
			v.write(t, s, g, "g")
			v.expect(t, s, g, "k", "g")
			v.expect(t, s, k, "k")
		})
	}
}

func TestLazyDirs_AnUncleanOpenRepairsAroundMissingDirs(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, lazyStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	notes := lazyVariants[0]
	notes.write(t, s, spawnTurns(t, s, 1), "other")
	p := spawnTurns(t, s, 2)
	k, err := s.Fork(p, mainTailOf(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	appendTurn(t, s, p)
	notes.write(t, s, p, "p")
	if err := s.Abandon(); err != nil {
		t.Fatal(err)
	}
	if !pathExists(uncleanPath(dir)) {
		t.Fatal("setup: abandon left the store clean")
	}
	s, err = OpenStore(dir, lazyStoreOptions())
	if err != nil {
		t.Fatalf("unclean open over missing channel dirs: %v", err)
	}
	defer s.Close()
	if got := nonMainDirs(t, s, dir, k); len(got) != 0 {
		t.Fatalf("repair created %v", got)
	}
	notes.expect(t, s, k)
	notes.expect(t, s, p, "p")
}

func TestLazyDirs_TheSweepNeitherReportsNorCreatesMissingDirs(t *testing.T) {
	s, root := openRegisteredLazyStore(t)
	p := spawnTurns(t, s, 2)
	k, err := s.Fork(p, mainTailOf(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.SweepUnmarkedNodes(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unmarked) != 0 || rep.Skipped != "" {
		t.Fatalf("sweep over lazy forks: %+v", rep)
	}
	if got := nonMainDirs(t, s, root, k); len(got) != 0 {
		t.Fatalf("sweep created %v", got)
	}
}

func TestLazyDirs_LineageWalkersSkipMissingDirs(t *testing.T) {
	s, root := openRegisteredLazyStore(t)
	notes := lazyVariants[0]
	p := spawnTurns(t, s, 3)
	k, err := s.Fork(p, mainTailOf(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	sib, err := s.Fork(p, mainTailOf(t, s, p))
	if err != nil {
		t.Fatal(err)
	}
	kNode, _ := s.HeadNode(k)
	pNode, _ := s.HeadNode(p)
	sibNode, _ := s.HeadNode(sib)

	if err := s.Trunks.sameInheritance(kNode, []string{sibNode}); err != nil {
		t.Fatalf("move check over a node with no channel dirs: %v", err)
	}
	if r, err := s.Retain(pNode, notes.channel, true); err != nil || len(r.Pins) != 0 {
		t.Fatalf("retention pins from a child with no dir: %+v, %v", r, err)
	}
	appendTurn(t, s, k)
	if ts := s.LastTS(TrunkID(k)); ts <= 0 {
		t.Fatalf("last timestamp of a fork with only a main dir: %d", ts)
	}
	if n := s.TrunkBytes(k, variantChannels); n != 0 {
		t.Fatalf("bytes of channels with no dir: %d", n)
	}
	e, err := s.Export(k, "0123abcd")
	if err != nil {
		t.Fatalf("export of a node with no channel dirs: %v", err)
	}
	e.Close()
	if err := s.Detach(kNode); err != nil {
		t.Fatalf("detach of a node with no channel dirs: %v", err)
	}
	notes.expect(t, s, k)
	if got := nonMainDirs(t, s, root, k); len(got) != 0 {
		t.Fatalf("the walkers created %v", got)
	}
	if err := s.Remove(k, false); err != nil {
		t.Fatalf("remove of a node with no channel dirs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ir", fsName(kNode))); !os.IsNotExist(err) {
		t.Fatalf("the removed node's main dir: %v", err)
	}
}
