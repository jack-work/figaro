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
	closed bool
}

func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{}
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
	return true
}

// SendSelfish/SendPatient are aliases for Send (legacy test compat).
func (b *Inbox) SendSelfish(evt event) bool { return b.Send(evt) }
func (b *Inbox) SendPatient(evt event)      { b.Send(evt) }

// Yield is a no-op (legacy compat).
func (b *Inbox) Yield() {}

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

// TakeUserPrompts removes and returns all queued user-prompt events (steering
// messages), leaving other events (e.g. control patches) in place. Non-blocking.
func (b *Inbox) TakeUserPrompts() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var taken, keep []event
	for _, e := range b.queue {
		if e.typ == eventUserPrompt {
			taken = append(taken, e)
		} else {
			keep = append(keep, e)
		}
	}
	b.queue = keep
	return taken
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
