package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentFirstReadsOfALibrettoReplayItOnce(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	var opens atomic.Int32
	real := openLibretto
	openLibretto = func(s *XwalStore, id string) (*Libretto, error) {
		opens.Add(1)
		time.Sleep(50 * time.Millisecond)
		return real(s, id)
	}
	t.Cleanup(func() { openLibretto = real })
	var wg sync.WaitGroup
	libs := make([]*Libretto, 8)
	for i := range libs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lib, err := be.Libretto(sourceID)
			if err != nil {
				t.Error(err)
			}
			libs[i] = lib
		}()
	}
	wg.Wait()
	if n := opens.Load(); n != 1 {
		t.Fatalf("8 concurrent first reads replayed the libretto %d times, want 1", n)
	}
	for i, lib := range libs {
		if lib != libs[0] {
			t.Fatalf("reader %d got a different libretto than reader 0", i)
		}
	}
}
