package actor

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var ErrLazyClosed = errors.New("actor: queue is closed")

type lazyState uint32

const (
	lazyIdle lazyState = iota
	lazyRunning
	lazyLingering
)

// Lazy is a single-writer queue whose goroutine exists only while there is
// work, lingering for an affinity window after draining.
type Lazy[T any] struct {
	mu     sync.Mutex
	items  []T
	closed bool

	st     atomic.Uint32
	wake   chan struct{}
	linger time.Duration
	batch  int
	drain  func([]T)

	spawns atomic.Uint64
}

// NewLazy: batch caps one drain (<=0 takes everything).
func NewLazy[T any](batch int, linger time.Duration, drain func([]T)) *Lazy[T] {
	return &Lazy[T]{wake: make(chan struct{}, 1), linger: linger, batch: batch, drain: drain}
}

// Submit never waits for the work.
func (q *Lazy[T]) Submit(v T) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrLazyClosed
	}
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.ensure()
	return nil
}

func (q *Lazy[T]) ensure() {
	for {
		switch lazyState(q.st.Load()) {
		case lazyRunning:
			return
		case lazyLingering:
			if q.st.CompareAndSwap(uint32(lazyLingering), uint32(lazyRunning)) {
				select {
				case q.wake <- struct{}{}:
				default:
				}
				return
			}
		case lazyIdle:
			if q.st.CompareAndSwap(uint32(lazyIdle), uint32(lazyRunning)) {
				q.spawns.Add(1)
				go q.work()
				return
			}
		}
	}
}

func (q *Lazy[T]) take() []T {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items)
	if n == 0 {
		return nil
	}
	if q.batch > 0 && q.batch < n {
		n = q.batch
	}
	out := q.items[:n:n]
	q.items = q.items[n:]
	return out
}

func (q *Lazy[T]) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *Lazy[T]) work() {
	timer := time.NewTimer(q.linger)
	defer timer.Stop()

	for {
		for {
			batch := q.take()
			if len(batch) == 0 {
				break
			}
			q.drain(batch)
		}

		if !q.st.CompareAndSwap(uint32(lazyRunning), uint32(lazyLingering)) {
			continue
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(q.linger)

		select {
		case <-q.wake:
			continue
		case <-timer.C:
			if !q.st.CompareAndSwap(uint32(lazyLingering), uint32(lazyIdle)) {
				continue
			}
			// A Submit between take() and the CAS saw a drainer and did not
			// spawn; re-check after declaring idle or its item sits unrun.
			if q.pending() > 0 && q.st.CompareAndSwap(uint32(lazyIdle), uint32(lazyRunning)) {
				continue
			}
			return
		}
	}
}

// Close refuses further submissions; what is queued still drains.
func (q *Lazy[T]) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.ensure()
}

func (q *Lazy[T]) Closed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

func (q *Lazy[T]) Spawns() uint64 { return q.spawns.Load() }

func (q *Lazy[T]) Running() bool { return lazyState(q.st.Load()) != lazyIdle }
