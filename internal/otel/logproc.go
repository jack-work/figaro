package otel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// A queue before a FILE buys nothing (an append is µs, no round trip to
// amortize); a queue before a COLLECTOR is what keeps a slow one off the
// turn goroutine. So capacity is the policy: 0 exports inline, >0 buffers.
func newLogProcessor(exp sdklog.Exporter, capacity int) sdklog.Processor {
	if capacity <= 0 {
		return sdklog.NewSimpleProcessor(exp)
	}
	return newBoundedProcessor(exp, capacity)
}

// boundedProcessor exports on its own goroutine from a fixed ring.
// Telemetry never applies backpressure: a full ring DROPS and counts.
type boundedProcessor struct {
	exp sdklog.Exporter

	mu   sync.Mutex
	ring []sdklog.Record
	head int // next write
	n    int // occupancy
	wake chan struct{}

	dropped atomic.Int64
	stop    chan struct{}
	done    chan struct{}
}

func newBoundedProcessor(exp sdklog.Exporter, capacity int) *boundedProcessor {
	p := &boundedProcessor{
		exp:  exp,
		ring: make([]sdklog.Record, capacity),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go p.run()
	return p
}

// OnEmit copies the record (the SDK reuses the caller's) and returns.
func (p *boundedProcessor) OnEmit(_ context.Context, r *sdklog.Record) error {
	p.mu.Lock()
	if p.n == len(p.ring) {
		// Drop the OLDEST: the newest record is the one describing the
		// failure someone is about to investigate.
		p.head = (p.head + 1) % len(p.ring)
		p.n--
		p.dropped.Add(1)
	}
	p.ring[(p.head+p.n)%len(p.ring)] = *r
	p.n++
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return nil
}

func (p *boundedProcessor) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			p.drain(context.Background())
			return
		case <-p.wake:
			p.drain(context.Background())
		}
	}
}

// drain exports what is queued, releasing the lock across the export so a
// slow exporter blocks nobody but itself.
func (p *boundedProcessor) drain(ctx context.Context) {
	for {
		p.mu.Lock()
		if p.n == 0 {
			p.mu.Unlock()
			return
		}
		batch := make([]sdklog.Record, p.n)
		for i := 0; i < p.n; i++ {
			batch[i] = p.ring[(p.head+i)%len(p.ring)]
		}
		p.head, p.n = 0, 0
		p.mu.Unlock()
		_ = p.exp.Export(ctx, batch)
	}
}

// Dropped is what the ring shed, for doctor mem: a lossy structure ships
// with its number or the loss is invisible.
func (p *boundedProcessor) Dropped() int64 { return p.dropped.Load() }

func (p *boundedProcessor) ForceFlush(ctx context.Context) error {
	p.drain(ctx)
	return p.exp.ForceFlush(ctx)
}

// Shutdown drains under a deadline: a clean stop keeps the last seconds, a
// wedged collector does not hold exit.
func (p *boundedProcessor) Shutdown(ctx context.Context) error {
	select {
	case <-p.stop:
		return nil
	default:
		close(p.stop)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	select {
	case <-p.done:
	case <-ctx.Done():
	}
	return p.exp.Shutdown(ctx)
}

// Enabled: the ring accepts every record; the level filter runs upstream.
func (p *boundedProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
