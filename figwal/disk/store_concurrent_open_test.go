package disk

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentStoreOpensOfOneDirNeverTruncateALiveWriter(t *testing.T) {
	for round := 0; round < 200; round++ {
		root := t.TempDir()
		dir := filepath.Join(root, "chan")
		seed, err := Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 50; i++ {
			if err := seed.Write(uint64(i), []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
				t.Fatal(err)
			}
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}

		s := NewStore()
		var wg sync.WaitGroup
		start := make(chan struct{})
		wrote := make(chan int, 8)
		var mu sync.Mutex
		next := uint64(51)
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				l, err := s.Open(dir, Options{})
				if err != nil {
					t.Error(err)
					return
				}
				ok := 0
				for k := 0; k < 20; k++ {
					mu.Lock()
					idx := next
					err := l.Write(idx, []byte(fmt.Sprintf(`{"i":%d}`, idx)))
					if err == nil {
						next++
						ok++
					} else {
						t.Errorf("round %d write %d: %v", round, idx, err)
					}
					mu.Unlock()
				}
				wrote <- ok
			}()
		}
		close(start)
		wg.Wait()
		close(wrote)
		total := 0
		for n := range wrote {
			total += n
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		chk, err := Open(dir, Options{})
		if err != nil {
			t.Fatalf("round %d reopen: %v", round, err)
		}
		if got := chk.LastIndex(); got != uint64(50+total) {
			t.Fatalf("round %d: last index %d, want %d", round, got, 50+total)
		}
		chk.Close()
		if t.Failed() {
			return
		}
	}
}
