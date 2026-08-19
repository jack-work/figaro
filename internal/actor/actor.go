// Package actor is the single-writer runtime: a FIFO queue, one goroutine
// draining it, and the close semantics that go with both.
package actor

import (
	"context"
	"sync"
)

// Coalescer folds the head of the queue into one item. It is how two users with
// one runtime keep their own semantics: prompts concatenate with sender tags,
// form patches merge in order.
type Coalescer[E any] interface {
	Coalesce(pending []E) (taken int, folded E)
}

// Queue is the runtime. The zero value is unusable; call Start.
type Queue[E any] struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []E
	closed  bool
	fold    Coalescer[E]
	done    chan struct{}
}

// Start returns a queue. A non-nil handle gets its own goroutine draining the
// queue in order: the form's writer. A nil handle means the OWNER drains, by
// calling Recv itself: the aria's turn loop does that, because what it does
// between items (run a turn, wait on a provider) is not a handler's business.
// Either way there is exactly one consumer, which is the whole point.
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

// Send enqueues an item. False means the queue is closed, a refusal, so a
// caller waiting on a reply is not left waiting forever.
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

// Recv blocks for the next item, folding the head when a Coalescer says to.
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

// Do runs fn against the pending queue under the lock, replacing it with what
// fn returns. It is the seam a user with its own bookkeeping needs, an inbox
// that must reorder, coalesce, or delete queued items by id: without a second
// copy of the runtime growing around it.
func (q *Queue[E]) Do(fn func(pending []E) []E) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = fn(q.pending)
	q.cond.Broadcast()
}

// Read runs fn against the pending queue under the lock without changing it.
func (q *Queue[E]) Read(fn func(pending []E)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	fn(q.pending)
}

// TakeWhile removes and returns the longest prefix satisfying keep. It is what
// draining "everything ready of this kind" means, and it never reorders: a
// prefix cannot jump an item that arrived before it.
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

// Len is how many items are waiting.
func (q *Queue[E]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// IsIdle reports an empty queue.
func (q *Queue[E]) IsIdle() bool { return q.Len() == 0 }

// Closed reports whether the queue refuses new items. It reads the done channel
// rather than the flag, so it is safe to call from INSIDE Do or Read: where the
// lock is already held, and where a user's own bookkeeping most wants to ask.
func (q *Queue[E]) Closed() bool {
	select {
	case <-q.done:
		return true
	default:
		return false
	}
}

// Close refuses future sends and unblocks the drain. What was ALREADY ACCEPTED
// is still delivered: Recv returns false only once the queue is empty.
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

// Done closes when the queue does.
func (q *Queue[E]) Done() <-chan struct{} { return q.done }

func (q *Queue[E]) dropLocked(n int) {
	copy(q.pending, q.pending[n:])
	q.pending = q.pending[:len(q.pending)-n]
}
