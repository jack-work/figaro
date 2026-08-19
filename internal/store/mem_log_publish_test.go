package store

import (
	"sync"
	"testing"
)

// MemLog publishes a successor per append, so READS take no lock -- and the
// writer lock is what a publish cannot replace: two appends must not claim the
// same LT, and a successor built from a stale state loses one of them.
func TestMemLogAppendsDoNotLoseEachOther(t *testing.T) {
	log := NewMemLog[int]()
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := log.Append(Entry[int]{Payload: i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	entries := log.Read()
	if len(entries) != n {
		t.Fatalf("appended %d entries, the log holds %d", n, len(entries))
	}
	seen := map[uint64]bool{}
	for _, e := range entries {
		if seen[e.LT] {
			t.Fatalf("LT %d was handed out twice", e.LT)
		}
		seen[e.LT] = true
		if got, ok := log.Lookup(e.FigaroLT); !ok || got.LT != e.LT {
			t.Fatalf("the index lost FigaroLT %d", e.FigaroLT)
		}
	}
}

// A reader holding a slice from before an append must not see the append, and
// must never see a half-written tail: the successor is a copy, so the old
// header stays exactly as long as it was.
func TestMemLogReadersAreNotDisturbedByAppends(t *testing.T) {
	log := NewMemLog[int]()
	for i := 0; i < 8; i++ {
		if _, err := log.Append(Entry[int]{Payload: i}); err != nil {
			t.Fatal(err)
		}
	}
	before := log.Read()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = log.Append(Entry[int]{Payload: 100 + i})
		}
	}()
	for i := 0; i < 2000; i++ {
		if len(before) != 8 {
			t.Fatalf("a reader's slice grew under it: %d", len(before))
		}
		for j, e := range before {
			if e.Payload != j {
				t.Fatalf("a reader's entry changed under it at %d: %d", j, e.Payload)
			}
		}
	}
	wg.Wait()
}
