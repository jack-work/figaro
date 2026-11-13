package angelus

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

var (
	outboxMax   = 16 << 20
	outboxStall = 30 * time.Second
	outboxFlush = lapseNoticeGrace
)

type fellBehind struct {
	queued  int
	stalled time.Duration
	budget  int
}

func (e *fellBehind) Error() string {
	switch {
	case e.stalled > 0:
		return fmt.Sprintf("figaro: dropped a connection that stopped reading: one write stalled %s with %d bytes waiting; reconnect and resync", e.stalled.Round(time.Second), e.queued)
	case e.budget > 0:
		return fmt.Sprintf("figaro: dropped the connection furthest behind (%d bytes) when all doors passed %d; reconnect and resync", e.queued, e.budget)
	}
	return fmt.Sprintf("figaro: dropped a connection that fell behind: %d bytes waiting, over %d; reconnect and resync", e.queued, outboxMax)
}

func (a *Angelus) outboxBudget() *outboxBudget {
	a.outboxOnce.Do(func() {
		n, err := a.Settings.EndpointOutboxBudget()
		if err != nil {
			slog.Warn("door outbox budget", "err", err, "using_mib", config.DefaultOutboxBudgetMiB)
			n = config.DefaultOutboxBudgetMiB << 20
		}
		a.outboxes = newOutboxBudget(n)
	})
	return a.outboxes
}

type outboxBudget struct {
	mu    sync.Mutex
	max   int
	total int
	held  map[*outbox]int

	beforeCharge func()
}

func newOutboxBudget(max int) *outboxBudget {
	return &outboxBudget{max: max, held: map[*outbox]int{}}
}

func (b *outboxBudget) charge(o *outbox, n int) []*outbox {
	if b.beforeCharge != nil {
		b.beforeCharge()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held[o] += n
	b.total += n
	var behind []*outbox
	for b.total > b.max {
		var worst *outbox
		most := -1
		for box, q := range b.held {
			if q > most {
				worst, most = box, q
			}
		}
		if worst == nil {
			break
		}
		b.total -= most
		delete(b.held, worst)
		behind = append(behind, worst)
	}
	return behind
}

func (b *outboxBudget) release(o *outbox, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q, ok := b.held[o]; ok {
		b.held[o] = q - n
		b.total -= n
	}
}

func (b *outboxBudget) forget(o *outbox) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total -= b.held[o]
	delete(b.held, o)
}

func (b *outboxBudget) waiting() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

type outbox struct {
	io.ReadWriteCloser
	drop   func(error)
	budget *outboxBudget

	mu       sync.Mutex
	frames   [][]byte
	queued   int
	inflight int
	writing  time.Time
	err      error
	drained  chan struct{}

	wake      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newOutbox(rwc io.ReadWriteCloser, budget *outboxBudget, drop func(error)) *outbox {
	o := &outbox{ReadWriteCloser: rwc, drop: drop, budget: budget, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go o.run()
	return o
}

func (o *outbox) Write(p []byte) (int, error) {
	o.mu.Lock()
	if o.err != nil {
		err := o.err
		o.mu.Unlock()
		return 0, err
	}
	if o.queued > 0 {
		var stalled time.Duration
		if !o.writing.IsZero() {
			stalled = time.Since(o.writing)
		}
		if stalled > outboxStall {
			o.mu.Unlock()
			return 0, o.trip(&fellBehind{queued: o.queued + len(p), stalled: stalled})
		}
		if o.queued+len(p) > outboxMax {
			o.mu.Unlock()
			return 0, o.trip(&fellBehind{queued: o.queued + len(p)})
		}
	}
	o.mu.Unlock()
	var mine error
	for _, box := range o.budget.charge(o, len(p)) {
		behind := box.waiting()
		if box == o {
			behind += len(p)
		}
		err := box.trip(&fellBehind{queued: behind, budget: o.budget.max})
		if box == o {
			mine = err
		}
	}
	if mine != nil {
		return 0, mine
	}
	o.mu.Lock()
	if o.err != nil {
		err := o.err
		o.mu.Unlock()
		o.budget.forget(o)
		return 0, err
	}
	o.frames = append(o.frames, bytes.Clone(p))
	o.queued += len(p)
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (o *outbox) waiting() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.queued
}

func (o *outbox) trip(err error) error {
	o.mu.Lock()
	if o.err != nil {
		o.mu.Unlock()
		return err
	}
	o.err = err
	o.frames, o.queued = nil, o.inflight
	o.mu.Unlock()
	o.budget.forget(o)
	go o.drop(err)
	return err
}

func (o *outbox) run() {
	for {
		select {
		case <-o.wake:
		case <-o.done:
			return
		}
		for {
			o.mu.Lock()
			if len(o.frames) == 0 || o.err != nil {
				if o.drained != nil {
					close(o.drained)
					o.drained = nil
				}
				o.mu.Unlock()
				break
			}
			next := o.frames[0]
			o.frames[0] = nil
			o.frames = o.frames[1:]
			o.writing, o.inflight = time.Now(), len(next)
			o.mu.Unlock()
			_, err := o.ReadWriteCloser.Write(next)
			o.mu.Lock()
			o.writing, o.inflight = time.Time{}, 0
			o.queued = max(o.queued-len(next), 0)
			if err != nil && o.err == nil {
				o.err = err
			}
			o.mu.Unlock()
			o.budget.release(o, len(next))
		}
	}
}

func (o *outbox) drain(within time.Duration) {
	o.mu.Lock()
	if o.queued == 0 || o.err != nil {
		o.mu.Unlock()
		return
	}
	if o.drained == nil {
		o.drained = make(chan struct{})
	}
	drained := o.drained
	o.mu.Unlock()
	t := time.NewTimer(within)
	defer t.Stop()
	select {
	case <-drained:
	case <-t.C:
	}
}

func (o *outbox) Close() error {
	o.drain(outboxFlush)
	var err error
	o.closeOnce.Do(func() {
		o.mu.Lock()
		if o.err == nil {
			o.err = net.ErrClosed
		}
		o.frames = nil
		o.mu.Unlock()
		o.budget.forget(o)
		close(o.done)
		err = o.ReadWriteCloser.Close()
	})
	return err
}
