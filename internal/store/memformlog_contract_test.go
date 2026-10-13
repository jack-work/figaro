package store

import (
	"fmt"
	"sync"
	"testing"
)

// MemFormLog's writer is single by construction and its readers are
// concurrent; the lock-free design publishes an immutable value. These two
// tests keep that from being an assumption.

// A second concurrent writer is a contract violation and must be reported.
func TestMemFormLogRefusesASecondConcurrentWriter(t *testing.T) {
	m := &MemFormLog{}
	release := make(chan struct{})
	entered := make(chan struct{})

	// A writer that parks INSIDE AppendPatch is not constructible from
	// outside, so the detector is exercised by overlapping two real appends
	// with a barrier: the first holds the flag while the second tries.
	m.testHold = func() {
		close(entered)
		<-release
	}
	var firstErr, secondErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = m.AppendPatch([]byte(`{"set":{"a":"1"}}`))
	}()
	<-entered
	_, secondErr = m.AppendPatch([]byte(`{"set":{"b":"2"}}`))
	close(release)
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("the serialized writer failed: %v", firstErr)
	}
	if secondErr == nil {
		t.Fatal("a SECOND CONCURRENT WRITER was accepted. The lock was removed on " +
			"the strength of a single-writer contract; if nothing reports the " +
			"contract breaking, the removal is an assumption rather than a design")
	}
}

// Readers run concurrently with the writer and must never observe a torn or
// short history. Run under -race.
func TestMemFormLogReadersSeeAConsistentHistoryDuringAppends(t *testing.T) {
	m := &MemFormLog{}
	const writes = 400

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < writes; i++ {
			if _, err := m.AppendPatch([]byte(fmt.Sprintf(`{"set":{"k":"%d"}}`, i))); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
		}
	}()

	for i := 0; i < 2000; i++ {
		seen := uint64(0)
		err := m.RangePatches(0, 0, func(idx uint64, payload []byte) error {
			seen++
			if idx != seen {
				return fmt.Errorf("index %d arrived at position %d: the history is not contiguous", idx, seen)
			}
			if len(payload) == 0 {
				return fmt.Errorf("record %d is empty: a reader observed a slot before its payload", idx)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	<-done
}
