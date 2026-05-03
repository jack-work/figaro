package figaro

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Inbox is the per-aria event bus. Implements provider.Bus.
//
// Owns the typed partitions (Figaro IR + translator cache) as data
// stores. Push enqueues provider events; SendSelfish/SendPatient
// enqueue control + figaro events. Recv dequeues, fires the routing
// subscriber to place the event on its stream, then returns it for
// the act loop's semantic dispatch.
type Inbox struct {
	Figaro     store.Stream[message.Message]
	Translator store.Stream[[]json.RawMessage]

	mu      sync.Mutex
	cond    *sync.Cond
	active  []event
	waiting []event
	yielded bool
	closed  bool

	subs []func(event)
}

func NewInbox(ctx context.Context, fig store.Stream[message.Message], translator store.Stream[[]json.RawMessage]) *Inbox {
	b := &Inbox{Figaro: fig, Translator: translator, yielded: true}
	b.cond = sync.NewCond(&b.mu)
	b.subs = append(b.subs, b.routeToStreams)
	go func() {
		<-ctx.Done()
		b.Close()
	}()
	return b
}

// routeToStreams places provider/figaro events on their respective
// streams as they're consumed. Other event types pass through.
func (b *Inbox) routeToStreams(ev event) {
	switch ev.typ {
	case eventFigaro:
		if b.Figaro != nil {
			b.Figaro.Append(store.Entry[message.Message]{Payload: ev.figMsg}, true)
		}
	case eventTranslatorLive:
		if b.Translator != nil {
			b.Translator.Append(store.Entry[[]json.RawMessage]{Payload: ev.translatorPayload}, false)
		}
	}
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

// Push implements provider.Bus. Every native event lands on the
// translator stream's live tail; condenseLive folds them at end-of-
// turn via Provider.Assemble.
func (b *Inbox) Push(ev provider.Event) {
	b.SendSelfish(event{typ: eventTranslatorLive, translatorPayload: ev.Payload})
}

// PublishFigaro queues a figaro IR event. Routed to figStream on Recv.
func (b *Inbox) PublishFigaro(msg message.Message) {
	b.SendSelfish(event{typ: eventFigaro, figMsg: msg})
}
