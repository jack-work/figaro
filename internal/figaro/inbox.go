package figaro

import (
	"context"
	"sync"
)

// Inbox is the per-aria user-RPC event queue.
type Inbox struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []event
	wake   chan struct{} // queue-change signal only; events remain in the FIFO
	closed bool
}

func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{wake: make(chan struct{}, 1)}
	b.cond = sync.NewCond(&b.mu)
	go func() {
		<-ctx.Done()
		b.Close()
	}()
	return b
}

// Send enqueues an event. Returns false if closed.
func (b *Inbox) Send(evt event) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.queue = append(b.queue, evt)
	b.cond.Signal()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *Inbox) Wake() <-chan struct{} { return b.wake }

func (b *Inbox) Recv() (event, bool) {
	b.mu.Lock()
	for len(b.queue) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed && len(b.queue) == 0 {
		b.mu.Unlock()
		return event{}, false
	}
	evt := b.queue[0]
	copy(b.queue, b.queue[1:])
	b.queue = b.queue[:len(b.queue)-1]
	b.mu.Unlock()
	return evt, true
}

// TakeReadyUserPrompts removes the contiguous user-prompt prefix. It never
// jumps prompts over an earlier control or fork event.
func (b *Inbox) TakeReadyUserPrompts() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for n < len(b.queue) && b.queue[n].typ == eventUserPrompt {
		n++
	}
	taken := append([]event(nil), b.queue[:n]...)
	copy(b.queue, b.queue[n:])
	b.queue = b.queue[:len(b.queue)-n]
	b.signalReadyForkLocked()
	return taken
}

// Prepend restores events that could not be durably processed.
func (b *Inbox) Prepend(events []event) bool {
	if len(events) == 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	queue := make([]event, 0, len(events)+len(b.queue))
	queue = append(queue, events...)
	queue = append(queue, b.queue...)
	b.queue = queue
	b.cond.Signal()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *Inbox) TakeReadyForks() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for n < len(b.queue) {
		if b.queue[n].typ != eventFork {
			break
		}
		n++
	}
	taken := append([]event(nil), b.queue[:n]...)
	copy(b.queue, b.queue[n:])
	b.queue = b.queue[:len(b.queue)-n]
	return taken
}

func (b *Inbox) signalReadyForkLocked() {
	if len(b.queue) == 0 || b.queue[0].typ != eventFork {
		return
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// IsIdle reports whether the inbox is empty (no events queued).
func (b *Inbox) IsIdle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue) == 0
}

func (b *Inbox) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}
