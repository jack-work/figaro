// Package actor is the single-writer runtime: a FIFO queue and one consumer.
package actor

import (
	"context"
	"sync"
)

// Coalescer folds the head of the queue into one item.
type Coalescer[E any] interface {
	Coalesce(pending []E) (taken int, folded E)
}

// Queue's zero value is unusable; call Start.
type Queue[E any] struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []E
	closed  bool
	fold    Coalescer[E]
	done    chan struct{}
}

// Start drains through handle on its own goroutine; a nil handle means the
// owner drains by calling Recv. Either way there is exactly one consumer.
func Start[E any](ctx context.Context, handle func(E), fold Coalescer[E]) *Queue[E] {
	q := &Queue[E]{fold: fold, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	if handle != nil {
		go func() {
			for {
				item, ok := q.Recv()
				if !ok {
					return
				}
				handle(item)
			}
		}()
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				q.Close()
			case <-q.done:
			}
		}()
	}
	return q
}

// Send returns false when the queue is closed.
func (q *Queue[E]) Send(item E) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.pending = append(q.pending, item)
	q.cond.Broadcast()
	return true
}

func (q *Queue[E]) Recv() (E, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.pending) == 0 && !q.closed {
		q.cond.Wait()
	}
	var zero E
	if len(q.pending) == 0 {
		return zero, false
	}
	if q.fold != nil {
		if taken, folded := q.fold.Coalesce(q.pending); taken > 0 {
			q.dropLocked(taken)
			return folded, true
		}
	}
	item := q.pending[0]
	q.dropLocked(1)
	return item, true
}

// Do replaces the pending queue with what fn returns, under the lock.
func (q *Queue[E]) Do(fn func(pending []E) []E) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = fn(q.pending)
	q.cond.Broadcast()
}

func (q *Queue[E]) Read(fn func(pending []E)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	fn(q.pending)
}

// TakeWhile removes the longest prefix satisfying keep; it never reorders.
func (q *Queue[E]) TakeWhile(keep func(E) bool) []E {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for n < len(q.pending) && keep(q.pending[n]) {
		n++
	}
	if n == 0 {
		return nil
	}
	taken := append([]E(nil), q.pending[:n]...)
	q.dropLocked(n)
	return taken
}

func (q *Queue[E]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *Queue[E]) IsIdle() bool { return q.Len() == 0 }

// Closed reads the done channel, not the flag, so it is safe inside Do or Read.
func (q *Queue[E]) Closed() bool {
	select {
	case <-q.done:
		return true
	default:
		return false
	}
}

// Close refuses future sends; what was already accepted is still delivered.
func (q *Queue[E]) Close() {
	q.mu.Lock()
	already := q.closed
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
	if !already {
		close(q.done)
	}
}

func (q *Queue[E]) Done() <-chan struct{} { return q.done }

func (q *Queue[E]) dropLocked(n int) {
	copy(q.pending, q.pending[n:])
	q.pending = q.pending[:len(q.pending)-n]
}
