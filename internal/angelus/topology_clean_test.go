package angelus

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/topo"
)

type fakeCleaner struct {
	dirty  bool
	cleans int
	err    error
}

func (f *fakeCleaner) TopologyDirty() bool { return f.dirty }

func (f *fakeCleaner) CleanTopology(dryRun bool) ([]topo.Stale, error) {
	if dryRun {
		panic("the deferred clean must not dry-run")
	}
	f.cleans++
	if f.err == nil {
		f.dirty = false
	}
	return nil, f.err
}

// The hourly pass costs nothing when neither the disk nor the overrides
// moved, and retries next time when a clean fails.
func TestDeferredCleanRunsOnlyWhenDirty(t *testing.T) {
	f := &fakeCleaner{}
	cleanTopologyIfDirty(f)
	if f.cleans != 0 {
		t.Fatal("cleaned a clean topology")
	}
	f.dirty, f.err = true, errors.New("form moved")
	cleanTopologyIfDirty(f)
	if f.cleans != 1 || !f.dirty {
		t.Fatalf("a failed clean must leave the topology dirty: cleans=%d dirty=%v", f.cleans, f.dirty)
	}
	f.err = nil
	cleanTopologyIfDirty(f)
	if f.cleans != 2 || f.dirty {
		t.Fatalf("cleans=%d dirty=%v after a good pass", f.cleans, f.dirty)
	}
}
