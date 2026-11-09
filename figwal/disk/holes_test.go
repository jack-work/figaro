package disk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func holedLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 40; i++ {
		if err := l.Write(i, []byte(fmt.Sprintf("rec-%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.sealed) < 5 {
		t.Fatalf("only %d sealed segments; the test needs several", len(l.sealed))
	}
	return l, dir
}

// The hazard this defends: segment ranges are inferred from the next file's
// name, so deleting a middle file used to stretch its neighbour over the gap
// and route reads to the wrong records.
func TestADroppedMiddleSegmentIsAHoleNotAMisroute(t *testing.T) {
	l, dir := holedLog(t)
	victim := l.sealed[2]
	keep := func(first, last uint64) bool { return first != victim.base }
	if dropped, err := l.DropSealed(keep); err != nil || len(dropped) != 1 {
		t.Fatalf("DropSealed = %v, %v", dropped, err)
	}
	check := func(l *Log, when string) {
		t.Helper()
		for i := uint64(1); i <= 40; i++ {
			got, err := l.Read(i)
			if i >= victim.base && i <= victim.last {
				if !errors.Is(err, ErrCompacted) {
					t.Fatalf("%s: read %d in the hole = %q, %v; want ErrCompacted", when, i, got, err)
				}
				continue
			}
			if err != nil || string(got) != fmt.Sprintf("rec-%02d", i) {
				t.Fatalf("%s: read %d = %q, %v", when, i, got, err)
			}
		}
	}
	check(l, "live")
	l.Close()
	l2, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	check(l2, "reopened")
}

func TestDroppingTheFrontLeavesNoStubAndMovesTheFirstIndex(t *testing.T) {
	l, dir := holedLog(t)
	defer l.Close()
	cut := l.sealed[2].base
	if _, err := l.DropSealed(func(first, last uint64) bool { return first >= cut }); err != nil {
		t.Fatal(err)
	}
	if got := l.FirstIndex(); got != cut {
		t.Fatalf("first index %d, want %d", got, cut)
	}
	stubs, _ := filepath.Glob(filepath.Join(dir, "*"+holeSuffix))
	if len(stubs) != 0 {
		t.Fatalf("stubs %v left at the front; a leading hole is plain deletion", stubs)
	}
}

func TestTheNewestSealedSegmentIsNeverDropped(t *testing.T) {
	l, _ := holedLog(t)
	defer l.Close()
	newest := l.sealed[len(l.sealed)-1]
	l.DropSealed(func(uint64, uint64) bool { return false })
	if newest.hole {
		t.Fatal("the newest sealed segment was dropped; the active segment's header is seeded from it")
	}
}

// A crash between writing the stub and unlinking the segment leaves both; the
// next open must finish the drop, not resurrect the records.
func TestACrashMidDropFinishesOnOpen(t *testing.T) {
	l, dir := holedLog(t)
	victim := l.sealed[1]
	path := victim.path
	l.Close()
	stub := filepath.Join(dir, filepath.Base(path)+holeSuffix)
	if err := os.WriteFile(stub, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if _, err := l2.Read(victim.base); !errors.Is(err, ErrCompacted) {
		t.Fatalf("read in a half-dropped segment = %v, want ErrCompacted", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the half-dropped segment file is still on disk")
	}
}

func TestReadersDuringADropSeeOnlyGoodRecordsOrTheHole(t *testing.T) {
	l, _ := holedLog(t)
	defer l.Close()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for i := uint64(1); i <= 40; i++ {
					got, err := l.Read(i)
					if err != nil && !errors.Is(err, ErrCompacted) && !errors.Is(err, ErrNotFound) {
						t.Errorf("read %d: %v", i, err)
						return
					}
					if err == nil && string(got) != fmt.Sprintf("rec-%02d", i) {
						t.Errorf("read %d = %q: a misrouted read", i, got)
						return
					}
				}
			}
		}()
	}
	for _, ss := range append([]*sealedSeg(nil), l.sealed...) {
		base := ss.base
		l.DropSealed(func(first, last uint64) bool { return first != base })
	}
	close(stop)
	wg.Wait()
}

func TestDroppableNamesWhatDropSealedWouldTake(t *testing.T) {
	l, _ := holedLog(t)
	defer l.Close()
	keep := func(first, last uint64) bool { return first%2 == 0 }
	plan := l.Droppable(keep)
	if len(plan) == 0 {
		t.Fatal("the plan is empty; the test needs something to drop")
	}
	for _, sp := range plan {
		if sp.Bytes <= 0 {
			t.Fatalf("span %+v carries no size", sp)
		}
	}
	got, err := l.DropSealed(keep)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(plan) {
		t.Fatalf("dropped %v, the plan said %v", got, plan)
	}
	if again := l.Droppable(keep); len(again) != 0 {
		t.Fatalf("after the drop the plan still names %v", again)
	}
}

func TestTheFloorIsTheTopOfTheHighestHole(t *testing.T) {
	l, _ := holedLog(t)
	defer l.Close()
	if f := l.Floor(); f != 0 {
		t.Fatalf("an intact log has floor %d", f)
	}
	low, high := l.sealed[1], l.sealed[3]
	if _, err := l.DropSealed(func(first, last uint64) bool { return first != low.base && first != high.base }); err != nil {
		t.Fatal(err)
	}
	if f := l.Floor(); f != high.last {
		t.Fatalf("floor %d, want %d (the top of the highest hole)", f, high.last)
	}
}

func TestAFrontDropRaisesTheFloor(t *testing.T) {
	l, _ := holedLog(t)
	defer l.Close()
	cut := l.sealed[2].base
	if _, err := l.DropSealed(func(first, last uint64) bool { return first >= cut }); err != nil {
		t.Fatal(err)
	}
	if f := l.Floor(); f != cut-1 {
		t.Fatalf("floor %d, want %d", f, cut-1)
	}
}

// A child sees only the parent's holes below its fork base: one above it is
// history the child never read.
func TestAChildFloorCountsOnlyTheParentHolesItReads(t *testing.T) {
	parent, _ := holedLog(t)
	defer parent.Close()
	below, above := parent.sealed[1], parent.sealed[4]
	fork := parent.sealed[3].base + 1
	childDir := filepath.Join(t.TempDir(), "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, ".fork"), []byte(fmt.Sprintf("base=%d\n", fork)), 0o644); err != nil {
		t.Fatal(err)
	}
	child, err := Open(childDir, Options{SegmentSize: 64, Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if _, err := parent.DropSealed(func(first, last uint64) bool { return first != above.base }); err != nil {
		t.Fatal(err)
	}
	if f := child.Floor(); f != 0 {
		t.Fatalf("a hole above the fork base gave the child floor %d", f)
	}
	if _, err := parent.DropSealed(func(first, last uint64) bool { return first != below.base }); err != nil {
		t.Fatal(err)
	}
	if f := child.Floor(); f != below.last {
		t.Fatalf("child floor %d, want %d", f, below.last)
	}
}

// A forked log's marker must line up with its first segment on open, so a
// front drop there keeps its stub instead of deleting the range outright.
func TestAFrontDropInAForkedLogReopens(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".fork"), []byte("base=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 40; i++ {
		if err := l.Write(i, []byte(fmt.Sprintf("rec-%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	cut := l.sealed[2].base
	if _, err := l.DropSealed(func(first, last uint64) bool { return first >= cut }); err != nil {
		t.Fatal(err)
	}
	if f := l.Floor(); f != cut-1 {
		t.Fatalf("floor %d, want %d", f, cut-1)
	}
	l.Close()
	l2, err := Open(dir, Options{SegmentSize: 64})
	if err != nil {
		t.Fatalf("reopen after a front drop: %v", err)
	}
	defer l2.Close()
	if f := l2.Floor(); f != cut-1 {
		t.Fatalf("reopened floor %d, want %d", f, cut-1)
	}
	if got, err := l2.Read(cut); err != nil || string(got) != fmt.Sprintf("rec-%02d", cut) {
		t.Fatalf("read %d = %q, %v", cut, got, err)
	}
}
