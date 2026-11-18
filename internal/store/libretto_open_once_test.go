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

func TestReadersWaitingOnAPanickedLibrettoOpenAreReleased(t *testing.T) {
	be, sourceID, _ := librettoFixture(t)
	real := openLibretto
	started := make(chan struct{})
	openLibretto = func(s *XwalStore, id string) (*Libretto, error) {
		close(started)
		time.Sleep(50 * time.Millisecond)
		panic("replay failed")
	}
	t.Cleanup(func() { openLibretto = real })
	go func() {
		defer func() { _ = recover() }()
		_, _ = be.Libretto(sourceID)
	}()
	<-started
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := be.Libretto(sourceID)
			errs <- err
		}()
	}
	for range 3 {
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("a reader waiting on a panicked open got a libretto")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a reader waiting on a panicked open never returned")
		}
	}
	openLibretto = real
	if _, err := be.Libretto(sourceID); err != nil {
		t.Fatalf("an open after the panicked one: %v", err)
	}
}
