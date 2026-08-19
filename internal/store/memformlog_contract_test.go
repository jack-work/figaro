package store

import (
	"fmt"
	"sync"
	"testing"
)

// THE CONTRACT, STATED AND THEN ASSERTED WHERE IT CAN FAIL.
//
// MemFormLog's writer is SINGLE by construction: AppendPatch is reached only
// from Form.reduceOne, which is reached only from Form.runBatch, which is the
// actor's one drainer. Its READERS are genuinely concurrent -- patchesFromLog
// (form.go:292) runs on whatever goroutine asks for a range.
//
// SO THE MUTEX WAS NOT DEFENSIVE CONCURRENCY FOR AN UNKNOWN CALLER, and this
// is the distinction the escalation rule turns on: the writer really is
// serialized, so writer-versus-writer exclusion is dead weight, but
// reader-versus-writer is REAL and may not simply be deleted. The cure is to
// PUBLISH AN IMMUTABLE VALUE, which removes the lock and KEEPS the concurrency.
//
// These two tests are what stop that from being an assumption.

// A SECOND CONCURRENT WRITER IS A CONTRACT VIOLATION AND MUST BE REPORTED,
// not tolerated. Deleting a lock on the strength of "the caller is serialized"
// is only safe if the code says so when the caller stops being serialized.
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

// AND THE HALF THAT MAY NOT BE DELETED: readers run concurrently with the
// writer, and must never observe a torn or short history. Run under -race.
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
