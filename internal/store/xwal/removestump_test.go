package xwal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A stump is the cauterization boundary: everything beneath it reads its birth
// record as a shared prefix. So it may only be removed once nothing is
// beneath it, and it must actually leave the tree when it goes.
func TestRemoveStumpDeletesAChildlessStump(t *testing.T) {
	dir := t.TempDir()
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("s@abc"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.Stumps()); got != 1 {
		t.Fatalf("stumps = %d, want 1", got)
	}

	if err := f.RemoveStump("s@abc"); err != nil {
		t.Fatalf("RemoveStump: %v", err)
	}
	if got := len(f.Stumps()); got != 0 {
		t.Fatalf("stumps after remove = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ir", "s@abc")); !os.IsNotExist(err) {
		t.Fatalf("stump directory survived: %v", err)
	}
}

// Removing a stump with a live trunk under it would strand that trunk's
// history. Refusing is deliberate: a caller checks Stumps() first, so getting
// here means the two raced, and a silent success would hide it.
func TestRemoveStumpRefusesWhileAChildRemains(t *testing.T) {
	dir := t.TempDir()
	f, id := seedTrunk(t, dir)

	err := f.RemoveStump("s")
	if !errors.Is(err, ErrStumpHasChildren) {
		t.Fatalf("err = %v, want ErrStumpHasChildren", err)
	}
	if got := len(f.Stumps()); got != 1 {
		t.Fatalf("stump was removed anyway: %d", got)
	}
	// And the child is still readable through it.
	x, err := f.Head(id)
	if err != nil {
		t.Fatalf("child head after refused remove: %v", err)
	}
	x.Close()
}

// The lifecycle figaro drives: the last child dies, then the stump goes.
func TestRemoveStumpAfterItsLastChildIsRemoved(t *testing.T) {
	dir := t.TempDir()
	f, id := seedTrunk(t, dir)

	if _, _, err := f.Append(string(id), 0, []byte(`{"m":"hello"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(string(id), true); err != nil {
		t.Fatalf("remove child: %v", err)
	}
	stumps := f.Stumps()
	if len(stumps) != 1 || len(stumps[0].Children) != 0 {
		t.Fatalf("stumps = %+v, want one childless", stumps)
	}
	if err := f.RemoveStump("s"); err != nil {
		t.Fatalf("RemoveStump: %v", err)
	}
	if got := len(f.Stumps()); got != 0 {
		t.Fatalf("stumps = %d, want 0", got)
	}
}

func TestRemoveStumpRejectsUnknownAndNonStumpNames(t *testing.T) {
	dir := t.TempDir()
	f, id := seedTrunk(t, dir)

	if err := f.RemoveStump("nope"); !errors.Is(err, ErrUnknownStump) {
		t.Fatalf("unknown: err = %v, want ErrUnknownStump", err)
	}
	// A trunk id is not a stump name, even though both address nodes.
	if err := f.RemoveStump(string(id)); !errors.Is(err, ErrUnknownStump) {
		t.Fatalf("trunk id: err = %v, want ErrUnknownStump", err)
	}
}

// The trap this had to be written around: a stump's head is opened on every
// fork beneath it, so the store can be holding a hot head for a directory that
// RemoveStump is about to unlink. If that head is not retired, the flusher
// writes it back and resurrects the stump, or poisons the store.
func TestRemoveStumpAfterItsHeadHasBeenOpened(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Trunks.CreateStump("s@abc"); err != nil {
		t.Fatal(err)
	}
	// Write birth content the way figaro does, then let it go.
	sx, err := s.Trunks.StumpHead("s@abc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sx.AppendMain([]byte(`{"m":"birth"}`), nil); err != nil {
		t.Fatal(err)
	}
	sx.Close()
	if err := s.SyncStump("s@abc"); err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveStump("s@abc"); err != nil {
		t.Fatalf("RemoveStump after an opened head: %v", err)
	}
	if got := len(s.Trunks.Stumps()); got != 0 {
		t.Fatalf("stumps = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ir", "s@abc")); !os.IsNotExist(err) {
		t.Fatalf("stump directory survived: %v", err)
	}

	// The store must still be usable, and the flusher must not resurrect it.
	if err := s.Trunks.CreateStump("s@def"); err != nil {
		t.Fatalf("store unusable after RemoveStump: %v", err)
	}
	id, err := s.Trunks.SpawnUnderStump("s@def")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(id), "ir", 0, []byte(`{"m":"after"}`), nil); err != nil {
		t.Fatal(err)
	}
	s.Kick()
	if err := s.SyncStump("s@def"); err != nil {
		t.Fatalf("sync after RemoveStump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ir", "s@abc")); !os.IsNotExist(err) {
		t.Fatalf("the flusher resurrected the removed stump: %v", err)
	}
}

// Removing one stump must not disturb its neighbours or their children.
func TestRemoveStumpLeavesSiblingsIntact(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, name := range []string{"keep@1", "drop@2"} {
		if err := s.Trunks.CreateStump(name); err != nil {
			t.Fatal(err)
		}
	}
	kept, err := s.Trunks.SpawnUnderStump("keep@1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(kept), "ir", 0, []byte(`{"m":"survivor"}`), nil); err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveStump("drop@2"); err != nil {
		t.Fatalf("RemoveStump: %v", err)
	}

	stumps := s.Trunks.Stumps()
	if len(stumps) != 1 || stumps[0].Name != "keep@1" {
		t.Fatalf("stumps = %+v, want only keep@1", stumps)
	}
	if len(stumps[0].Children) != 1 {
		t.Fatalf("survivor lost its parent link: %+v", stumps[0])
	}
	x, err := s.Trunks.Head(kept)
	if err != nil {
		t.Fatalf("survivor head: %v", err)
	}
	x.Close()
}
