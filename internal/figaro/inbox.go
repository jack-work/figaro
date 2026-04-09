package figaro

import (
	"context"
	"sync"
)

// Inbox is a priority mailbox for the agent's actor loop.
//
// Events have two attitudes:
//   - Selfish: delivered immediately (LLM deltas, tool results, interrupts)
//   - Patient: held until the current turn yields (user prompts)
//
// The drain loop calls Recv in a loop. It only sees events when they're
// ready — patient events are invisible until Yield releases them.
type Inbox struct {
	mu      sync.Mutex
	cond    *sync.Cond
	active  []event // selfish events + released patient events
	waiting []event // patient events held until Yield
	yielded bool    // true = idle, next patient goes straight to active
	closed  bool
}

// NewInbox creates an inbox that closes automatically when ctx is cancelled.
// Starts in the yielded state (idle — first patient message delivers immediately).
func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{yielded: true}
	b.cond = sync.NewCond(&b.mu)
	go func() {
		<-ctx.Done()
		b.Close()
	}()
	return b
}

// SendSelfish appends an event to the active queue. It is delivered
// on the next Recv call regardless of turn state. Returns false if
// the inbox is closed (caller should exit).
func (b *Inbox) SendSelfish(evt event) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.active = append(b.active, evt)
	b.cond.Signal()
	return true
}

// SendPatient enqueues an event that waits for the current turn to
// yield. If the inbox is already yielded (idle), the event is
// delivered immediately and the inbox transitions to busy.
func (b *Inbox) SendPatient(evt event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.yielded {
		b.active = append(b.active, evt)
		b.yielded = false
	} else {
		b.waiting = append(b.waiting, evt)
	}
	b.cond.Signal()
}

// Yield signals that the current turn is complete. If patient events
// are waiting, the first one is released to active (starting the next
// turn). Otherwise the inbox transitions to the yielded (idle) state.
func (b *Inbox) Yield() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.waiting) > 0 {
		b.active = append(b.active, b.waiting[0])
		b.waiting = b.waiting[1:]
	} else {
		b.yielded = true
	}
	b.cond.Signal()
}

// Recv blocks until an event is available in the active queue.
// Returns (event, false) if the inbox is closed.
func (b *Inbox) Recv() (event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.active) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed {
		return event{}, false
	}
	evt := b.active[0]
	// Shift without retaining a reference to the old backing array.
	copy(b.active, b.active[1:])
	b.active = b.active[:len(b.active)-1]
	return evt, true
}

// IsIdle returns true if no turn is in progress.
func (b *Inbox) IsIdle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.yielded
}

// Close shuts down the inbox, unblocking any Recv call. Idempotent.
func (b *Inbox) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.cond.Broadcast()
}
