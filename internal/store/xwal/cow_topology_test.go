package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// I1, peers: a peer whose snapshot predates the removal cannot borrow the
// node, and a head a peer opened before it holds the directories like any
// other.
func TestAPeerCannotBorrowARemovedNodeDuringTheDrain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, victim := seedTrunk(t, dir)
	if _, _, err := f.Append(victim, 0, []byte(`"m1"`), nil); err != nil {
		t.Fatal(err)
	}
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	node := f.head(victim)
	held, err := peer.Head(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(victim, true); err != nil {
		held.Close()
		t.Fatal(err)
	}
	assertRemovedButHeld(t, f, victim, node, 1)
	if _, _, err := peer.borrowHotUntracked([]string{node}); !errors.Is(err, ErrUnknownTrunk) {
		held.Close()
		t.Fatalf("a stale peer borrowed the removed node: %v", err)
	}
	if _, err := peer.Head(victim); !errors.Is(err, ErrUnknownTrunk) {
		held.Close()
		t.Fatalf("a peer opened the removed trunk: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	assertDrained(t, f, node)
}

// I3: a fork reading its parent's segments flat keeps reading them through a
// removal of the parent's subtree, and the directories go only after it closes.
func TestAForkReadingItsParentFlatKeepsItsSegmentsThroughTheRemoval(t *testing.T) {
	f, parent := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	for i := 0; i < 8; i++ {
		if _, _, err := f.Append(parent, 0, []byte(fmt.Sprintf(`"p%d"`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	child, err := f.ForkTail(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(child, 0, []byte(`"c"`), nil); err != nil {
		t.Fatal(err)
	}
	before := mainPayloads(t, f, child)
	held, err := f.Head(child)
	if err != nil {
		t.Fatal(err)
	}
	parentNode, childNode := f.head(parent), f.head(child)
	if err := f.Remove(parent, true); err != nil {
		held.Close()
		t.Fatal(err)
	}
	var after []string
	l, err := held.channel(held.main).Log()
	if err != nil {
		held.Close()
		t.Fatal(err)
	}
	for i := uint64(1); i <= l.LastIndex(); i++ {
		_, p, err := held.Read(held.main, i)
		if err != nil {
			held.Close()
			t.Fatalf("reading through the removed parent at %d: %v", i, err)
		}
		after = append(after, string(p))
	}
	if !slices.Equal(before, after) {
		held.Close()
		t.Fatalf("the fork read %v during the removal, want %v", after, before)
	}
	if len(nodeDirs(t, f, parentNode)) == 0 {
		held.Close()
		t.Fatal("the parent's directories went while its fork still reads them")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	assertDrained(t, f, parentNode)
	assertDrained(t, f, childNode)
}

// I1: a fork racing a removal either lands first and is taken with the
// subtree, or is refused; nothing is left pointing at a removed node.
func TestAForkRacingARemovalLeavesNoOrphan(t *testing.T) {
	for round := 0; round < 40; round++ {
		f, victim := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
		if _, _, err := f.Append(victim, 0, []byte(`"m1"`), nil); err != nil {
			t.Fatal(err)
		}
		node := f.head(victim)
		var wg sync.WaitGroup
		var forkErr, removeErr error
		var child string
		wg.Add(2)
		go func() { defer wg.Done(); child, forkErr = f.ForkTail(victim) }()
		go func() { defer wg.Done(); removeErr = f.Remove(victim, true) }()
		wg.Wait()
		if removeErr != nil {
			t.Fatalf("round %d: remove: %v", round, removeErr)
		}
		if forkErr == nil && f.hasHead(child) {
			t.Fatalf("round %d: fork %s survived the removal of its parent", round, child)
		}
		if forkErr != nil && !errors.Is(forkErr, ErrUnknownTrunk) {
			t.Fatalf("round %d: fork failed with %v", round, forkErr)
		}
		for key, n := range f.idx.All() {
			if n.From != "" && f.node(n.From) == nil {
				t.Fatalf("round %d: node %s points at removed %s", round, key, n.From)
			}
		}
		assertDrained(t, f, node)
	}
}

// I2: a reader never sees half a removal: no node whose parent is gone.
func TestAListingNeverSeesHalfARemoval(t *testing.T) {
	f, root := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	var victims []string
	for i := 0; i < 20; i++ {
		v, err := f.ForkTail(root)
		if err != nil {
			t.Fatal(err)
		}
		for d := 0; d < 3; d++ {
			if _, err := f.ForkTail(v); err != nil {
				t.Fatal(err)
			}
		}
		victims = append(victims, v)
	}
	stop := make(chan struct{})
	bad := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			all := f.idx.All()
			for key, n := range all {
				if n.From == "" {
					continue
				}
				if _, ok := all[n.From]; !ok {
					select {
					case bad <- key + " -> " + n.From:
					default:
					}
					return
				}
			}
		}
	}()
	for _, v := range victims {
		if err := f.Remove(v, true); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case b := <-bad:
		t.Fatalf("a listing saw a node whose parent was half removed: %s", b)
	default:
	}
}

// Settled point 5: a cut fenced on a node holds off its removal, and a cut
// on a removed node refuses.
func TestACutAndARemovalOnOneNodeSerialize(t *testing.T) {
	t.Run("cut first", func(t *testing.T) {
		f, victim := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
		node := f.head(victim)
		endCut, err := f.beginCut(node)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- f.Remove(victim, true) }()
		select {
		case err := <-done:
			endCut()
			t.Fatalf("the removal did not wait for the cut: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		endCut()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		assertDrained(t, f, node)
	})
	t.Run("removal first", func(t *testing.T) {
		f, victim := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
		node := f.head(victim)
		if err := f.Remove(victim, true); err != nil {
			t.Fatal(err)
		}
		if _, err := f.beginCut(node); !errors.Is(err, ErrUnknownTrunk) {
			t.Fatalf("a cut on a removed node: %v, want ErrUnknownTrunk", err)
		}
		err := f.cutChannel(victim, "translations", func(x *XWAL) error { return x.Clear("translations") })
		if !errors.Is(err, ErrUnknownTrunk) {
			t.Fatalf("cutChannel on a removed trunk: %v, want ErrUnknownTrunk", err)
		}
		assertDrained(t, f, node)
	})
}

// A head that never closes holds the directories: the drain says so once,
// reports the removal as pending, and finishes when the head returns.
func TestAHeldHeadDelaysTheUnlinkAndSaysSo(t *testing.T) {
	old := removalWarnAfter.Swap(int64(10 * time.Millisecond))
	t.Cleanup(func() { removalWarnAfter.Store(old) })
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f, victim := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	held, err := f.Head(victim)
	if err != nil {
		t.Fatal(err)
	}
	node := f.head(victim)
	if err := f.Remove(victim, true); err != nil {
		held.Close()
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	f.drainRemovals()
	f.drainRemovals()
	if n := strings.Count(logs.String(), "a removed node is still read"); n != 1 {
		held.Close()
		t.Fatalf("held-head warnings = %d, want 1:\n%s", n, logs.String())
	}
	p := f.PendingRemovals()
	if len(p) != 1 || p[0].Heads != 1 || time.Since(p[0].Since) < 10*time.Millisecond || !slices.Contains(p[0].Nodes, node) {
		held.Close()
		t.Fatalf("pending removals = %+v", p)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	assertDrained(t, f, node)
}

// A stump name comes back (an outfit version minted again): it cannot be
// created over a removal still waiting on a reader, and once created the old
// removal's drain must not take the new stump's directories.
func TestAStumpNameIsNotReusedOverAPendingRemoval(t *testing.T) {
	f, _ := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	if err := f.CreateStump("v1"); err != nil {
		t.Fatal(err)
	}
	held, err := f.StumpHead("v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveStump("v1"); err != nil {
		held.Close()
		t.Fatal(err)
	}
	if err := f.CreateStump("v1"); err == nil || !strings.Contains(err.Error(), "still being removed") {
		held.Close()
		t.Fatalf("CreateStump over a pending removal: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateStump("v1"); err != nil {
		t.Fatalf("CreateStump after the reader closed: %v", err)
	}
	f.drainRemovals()
	if len(nodeDirs(t, f, "v1")) == 0 {
		t.Fatal("the drain took the new stump's directories")
	}
	x, err := f.StumpHead("v1")
	if err != nil {
		t.Fatalf("the new stump: %v", err)
	}
	x.Close()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
