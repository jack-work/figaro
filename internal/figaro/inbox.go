package figaro

import (
	"context"
	"sync"
)

// Inbox is the per-aria user-RPC event queue. SendPatient and
// SendSelfish are aliases for "enqueue" — kept so existing test
// helpers compile. Recv dequeues for the act loop. Provider deltas,
// tool events, and turn-progress signals do NOT flow here — runTurn
// owns those internally.
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

// Send enqueues an event. False when the inbox has been closed.
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

// SendSelfish / SendPatient are kept as method names for test
// helpers; both behave identically now (FIFO).
func (b *Inbox) SendSelfish(evt event) bool { return b.Send(evt) }
func (b *Inbox) SendPatient(evt event)      { b.Send(evt) }

// Yield is a no-op left in place for callers that still reference
// it. The Patient/Selfish/Yield gating from the pre-runTurn world
// is gone.
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
