package figaro

import (
	"context"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// Inbox is the per-aria event queue. SendSelfish and SendPatient
// enqueue; Recv dequeues for the act loop. The provider's bus surface
// (PushDelta / PushFigaro) maps to selfish events on this queue.
type Inbox struct {
	mu      sync.Mutex
	cond    *sync.Cond
	active  []event
	waiting []event
	yielded bool
	closed  bool

	subs []func(event)
}

func NewInbox(ctx context.Context) *Inbox {
	b := &Inbox{yielded: true}
	b.cond = sync.NewCond(&b.mu)
	go func() {
		<-ctx.Done()
		b.Close()
	}()
	return b
}

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

func (b *Inbox) Recv() (event, bool) {
	b.mu.Lock()
	for len(b.active) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed {
		b.mu.Unlock()
		return event{}, false
	}
	evt := b.active[0]
	copy(b.active, b.active[1:])
	b.active = b.active[:len(b.active)-1]
	subs := append([]func(event){}, b.subs...)
	b.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(evt)
		}
	}
	return evt, true
}

func (b *Inbox) IsIdle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.yielded
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

func (b *Inbox) Subscribe(fn func(event)) func() {
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	idx := len(b.subs) - 1
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if idx < len(b.subs) {
			b.subs[idx] = nil
		}
		b.mu.Unlock()
	}
}

// PushDelta implements provider.Bus — emits a figaro IR content block
// delta as a selfish event for the act loop to fan out to subscribers.
func (b *Inbox) PushDelta(c message.Content) {
	b.SendSelfish(event{typ: eventFigaroDelta, deltaText: c.Text, deltaCT: c.Type})
}

// PushFigaro implements provider.Bus — signals an assembled assistant
// message has been appended to figStream. The act loop fans out a
// stream.message notification and dispatches any tool calls.
func (b *Inbox) PushFigaro(msg message.Message) {
	b.SendSelfish(event{typ: eventFigaro, figMsg: msg})
}
