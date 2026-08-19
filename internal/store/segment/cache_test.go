package segment

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The accounting must survive an append racing an eviction: the evictor CASes
// the pointer it read, and an extended block means it lost. Dropping the
// segment from the held set on that path charged bytes to something nothing
// could ever evict, and the budget then shrank for the process's life.
func TestCacheAccountingSurvivesAppendVersusEvict(t *testing.T) {
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(1 << 20)

	dir := t.TempDir()
	s, err := Create(filepath.Join(dir, "seg"), BinaryCodec{}, 1, 1<<24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := make([]byte, 256)
	for i := 0; i < 64; i++ {
		if _, err := s.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReadIndex(0); err != nil { // load the block
		t.Fatal(err)
	}
	// A Segment's own state is serialized by disk.Log's lock; the EVICTOR is
	// the one thing that touches it from outside, which is the interleaving
	// worth testing. `lock` stands in for that lock.
	var lock sync.Mutex
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			lock.Lock()
			_, err := s.Append(payload)
			lock.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			lock.Lock()
			_, err := s.ReadIndex(0)
			lock.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			payloadBudget.TrimIdle(-1)
		}
	}()
	wg.Wait()

	// The property the old CAS-race test protected, restated for the
	// tree seat: however extend, evict and reload interleave, the
	// accounting must return to consistency -- an evict returns ALL the
	// run's bytes, a reload charges exactly the fresh block, and nothing
	// is stranded.
	if _, err := s.ReadIndex(0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(payload); err != nil { // extendTail widens the run in place
		t.Fatal(err)
	}
	if CachedRanges() == 0 {
		t.Fatal("append did not extend the tail run; the case under test cannot arise")
	}
	s.DropCache()
	if got := CachedBytes(); got != 0 {
		t.Fatalf("after dropping the only cached segment, %d bytes stranded on the meter", got)
	}
	if _, err := s.ReadIndex(0); err != nil { // reload
		t.Fatal(err)
	}
	// THE ACCOUNTING IS TREE'S NOW, so the assertion is against tree rather
	// than against a second copy this package used to keep: something is
	// resident and the meter agrees it is non-zero.
	if CachedRanges() == 0 {
		t.Fatal("reload after drop did not cache")
	}
	if got := CachedBytes(); got <= 0 {
		t.Fatalf("reload cached %d ranges but the meter reads %d bytes", CachedRanges(), got)
	}

	// Whatever the interleaving, the counter must describe what is held.
	payloadBudget.TrimIdle(-1)
	if got := CachedBytes(); got != 0 {
		t.Fatalf("after evicting everything, %d bytes still charged", got)
	}
	if got := CachedRanges(); got != 0 {
		t.Fatalf("after evicting everything, %d ranges still held", got)
	}
	_ = os.Remove(filepath.Join(dir, "seg"))
}

// The budget bounds a BUSY process. SweepIdle is the other half: a process
// that has gone quiet gives the memory back, which is what an idle clock is
// for. A block read since the cutoff must survive; one that has not must not.
func TestSweepIdleDropsWhatNobodyReads(t *testing.T) {
	old := CacheBudget()
	defer SetCacheBudget(old)
	SetCacheBudget(8 << 20)

	dir := t.TempDir()
	mk := func(name string) *Segment {
		s, err := Create(filepath.Join(dir, name), BinaryCodec{}, 1, 1<<24)
		if err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, 512)
		for i := 0; i < 16; i++ {
			if _, err := s.Append(payload); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.ReadIndex(0); err != nil { // load its block
			t.Fatal(err)
		}
		return s
	}
	hot, cold := mk("hot"), mk("cold")
	defer hot.Close()
	defer cold.Close()
	if CachedRanges() < 2 {
		t.Fatalf("fixture loaded %d ranges, want at least 2", CachedRanges())
	}
	before := CachedBytes()

	// One sweep with nothing read: both are still within the keep window.
	if dropped, _ := SweepIdle(2); dropped != 0 {
		t.Fatalf("first sweep dropped %d blocks, want 0", dropped)
	}
	// Read the hot one, then sweep past the window twice.
	for i := 0; i < 3; i++ {
		if _, err := hot.ReadIndex(0); err != nil {
			t.Fatal(err)
		}
		SweepIdle(2)
	}
	// The hot segment must still answer from cache and the cold one must not:
	// asserted through the READ rather than through a private pointer, which
	// is the only handle left now that the index lives in tree.
	if _, ok := hot.cachedPayload(0); !ok {
		t.Fatal("a range read on every sweep was dropped as idle")
	}
	if n := CachedRanges(); n == 0 {
		t.Fatalf("everything was swept (%d ranges); the hot one should survive", n)
	}
	if got := CachedBytes(); got >= before {
		t.Fatalf("sweeping freed nothing: %d bytes held, was %d", got, before)
	}
	// And what was dropped still reads.
	p, err := cold.ReadIndex(3)
	if err != nil || len(p) != 512 {
		t.Fatalf("swept segment no longer reads: %v", err)
	}
}
