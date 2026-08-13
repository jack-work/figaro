package actor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The exit race is the only subtle thing in lazy.go: a Submit landing between
// "queue is empty" and the goroutine declaring itself idle. With a zero
// linger the window is as wide as it gets.
func TestLazyExitRaceLosesNothing(t *testing.T) {
	const writers, each = 16, 400

	var got atomic.Int64
	q := NewLazy[int](8, 0, func(b []int) { got.Add(int64(len(b))) })

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := q.Submit(i); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for got.Load() < writers*each && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := got.Load(); n != writers*each {
		t.Fatalf("drained %d of %d: an item was enqueued with nobody to run it", n, writers*each)
	}
}

// Exactly one drainer at a time is what makes this a serialization point.
func TestLazyOneDrainerAtATime(t *testing.T) {
	var inside atomic.Int32
	var wg sync.WaitGroup
	q := NewLazy[int](4, time.Millisecond, func(b []int) {
		if inside.Add(1) != 1 {
			t.Error("two drainers ran at once")
		}
		time.Sleep(50 * time.Microsecond)
		inside.Add(-1)
		wg.Add(-len(b))
	})
	wg.Add(500)
	for i := 0; i < 500; i++ {
		if err := q.Submit(i); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

// The goroutine must leave when idle, and come back when work arrives. This
// is the whole reason the type exists.
func TestLazyGoesDormantAndReturns(t *testing.T) {
	done := make(chan struct{}, 4)
	q := NewLazy[int](1, 5*time.Millisecond, func(b []int) { done <- struct{}{} })

	if err := q.Submit(1); err != nil {
		t.Fatal(err)
	}
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for q.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q.Running() {
		t.Fatal("still running long after the linger window")
	}
	first := q.Spawns()

	if err := q.Submit(2); err != nil {
		t.Fatal(err)
	}
	<-done
	if q.Spawns() <= first {
		t.Fatal("a submit to a dormant queue did not spawn a drainer")
	}
}

// A burst inside the linger window must not spawn per item: that is the cost
// the affinity window exists to avoid.
func TestLazyBurstSpawnsOnce(t *testing.T) {
	var seen atomic.Int64
	q := NewLazy[int](32, 200*time.Millisecond, func(b []int) { seen.Add(int64(len(b))) })
	for i := 0; i < 200; i++ {
		if err := q.Submit(i); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for seen.Load() < 200 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := q.Spawns(); n != 1 {
		t.Fatalf("a burst spawned %d drainers, want 1", n)
	}
}

func TestLazyCloseRefusesAndDrains(t *testing.T) {
	var seen atomic.Int64
	q := NewLazy[int](4, time.Millisecond, func(b []int) { seen.Add(int64(len(b))) })
	for i := 0; i < 10; i++ {
		if err := q.Submit(i); err != nil {
			t.Fatal(err)
		}
	}
	q.Close()
	if err := q.Submit(11); err != ErrLazyClosed {
		t.Fatalf("a closed queue accepted work: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for seen.Load() < 10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if seen.Load() != 10 {
		t.Fatalf("close dropped queued work: drained %d of 10", seen.Load())
	}
}

// Order is FIFO within the queue, which the form's IfVersion guard relies on.
func TestLazyPreservesOrder(t *testing.T) {
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	q := NewLazy[int](3, time.Millisecond, func(b []int) {
		mu.Lock()
		order = append(order, b...)
		if len(order) == 50 {
			close(done)
		}
		mu.Unlock()
	})
	for i := 0; i < 50; i++ {
		if err := q.Submit(i); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	for i, v := range order {
		if v != i {
			t.Fatalf("out of order at %d: got %d", i, v)
		}
	}
}

func FuzzLazyInterleavings(f *testing.F) {
	f.Add(uint8(3), uint8(7), uint8(0))
	f.Add(uint8(1), uint8(64), uint8(9))
	f.Fuzz(func(t *testing.T, writers, batch, lingerUS uint8) {
		w := int(writers)%8 + 1
		b := int(batch)%16 + 1

		var seen atomic.Int64
		q := NewLazy[int](b, time.Duration(lingerUS)*time.Microsecond,
			func(x []int) { seen.Add(int64(len(x))) })

		var wg sync.WaitGroup
		for i := 0; i < w; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					_ = q.Submit(j)
				}
			}()
		}
		wg.Wait()
		deadline := time.Now().Add(5 * time.Second)
		for seen.Load() < int64(w*20) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if seen.Load() != int64(w*20) {
			t.Fatalf("lost work: %d of %d", seen.Load(), w*20)
		}
	})
}
