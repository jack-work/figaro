// Package pacer smooths bursty provider deltas into a steady,
// character-by-character stream for terminal display.
//
// A *Pacer wraps an io.Writer (typically a *largo.Writer) and exposes
// Push, Flush, and Close. Calls to Push are non-blocking — text is
// queued; a background goroutine drains the queue at a configured
// target rate (characters per second), writing rune-by-rune to the
// underlying writer.
//
// Why pacing?
//
// Provider SSE streams (Anthropic, OpenAI) emit chunks of wildly
// varying size: sometimes a single byte, sometimes 60+ at once.
// Writing each chunk straight to the terminal makes the output feel
// uneven — long stalls punctuated by bursts. Pacing converts this
// into a steady "typewriter" cadence the user reads as the model
// thinking out loud.
//
// First-byte latency is preserved. The pacer fast-paths the very
// first runes of a turn (until firstByteBypass elapses) so TTFT is
// not delayed. After that window, runes are emitted at the target
// rate. If the input queue grows past a soft cap, the pacer
// adaptively speeds up — emitting multiple runes per tick — to keep
// on-screen latency bounded.
//
// Concurrency model:
//   - Push is safe from any goroutine and never blocks.
//   - The background drainer is the only goroutine that writes to
//     out. Callers must not write to out directly while the pacer is
//     running; Flush guarantees the queue is drained synchronously
//     before returning, after which direct writes are safe again
//     until the next Push.
//   - Close stops the drainer and flushes any remaining runes. It
//     must be called or the goroutine leaks.
package pacer

import (
	"io"
	"sync"
	"time"
)

// Options configures a Pacer.
type Options struct {
	// TargetCPS is the steady-state emission rate in characters per
	// second. Typical values: 150–400. 0 disables pacing entirely
	// (Push writes synchronously to out).
	TargetCPS int

	// MaxLagRunes is a soft cap on queued characters. When exceeded,
	// the pacer emits multiple runes per tick to catch up. 0 picks a
	// reasonable default proportional to TargetCPS.
	MaxLagRunes int

	// FirstByteBypass is the duration after Start during which Push
	// writes synchronously instead of queuing. Preserves TTFT. 0
	// disables the bypass.
	FirstByteBypass time.Duration
}

// Pacer drains queued runes into out at a bounded rate.
type Pacer struct {
	out  io.Writer
	opts Options

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []rune
	closed bool

	startedAt    time.Time
	pastBypass   bool
	stopCh       chan struct{}
	drainedCh    chan struct{}
}

// New creates and starts a Pacer. Call Close (or Flush + Close) when
// done. If TargetCPS <= 0, pacing is disabled and Push writes
// synchronously to out.
func New(out io.Writer, opts Options) *Pacer {
	if opts.MaxLagRunes <= 0 && opts.TargetCPS > 0 {
		// Roughly 1 second's worth of runes at target rate is the
		// soft cap before adaptive speedup kicks in. Below that the
		// pacer feels steady; above it, the user perceives "the
		// model is way ahead of the screen" so we accelerate.
		opts.MaxLagRunes = opts.TargetCPS
	}
	p := &Pacer{
		out:       out,
		opts:      opts,
		stopCh:    make(chan struct{}),
		drainedCh: make(chan struct{}),
		startedAt: time.Now(),
	}
	p.cond = sync.NewCond(&p.mu)
	if opts.TargetCPS > 0 {
		go p.run()
	} else {
		// Disabled: signal "drained" immediately so Close is fast.
		close(p.drainedCh)
	}
	return p
}

// Push enqueues s for paced emission. Returns immediately. If the
// pacer was created with TargetCPS <= 0, Push writes synchronously
// to out and is equivalent to out.Write([]byte(s)).
func (p *Pacer) Push(s string) {
	if s == "" {
		return
	}
	if p.opts.TargetCPS <= 0 {
		p.out.Write([]byte(s))
		return
	}
	// First-byte bypass: if we're still within the bypass window AND
	// the queue is empty, pass straight through. This avoids the "is
	// it alive?" gap on TTFT. As soon as we've passed the window or
	// queued anything, all subsequent text goes through the queue.
	p.mu.Lock()
	if !p.pastBypass {
		if p.opts.FirstByteBypass > 0 && time.Since(p.startedAt) < p.opts.FirstByteBypass && len(p.buf) == 0 {
			p.mu.Unlock()
			p.out.Write([]byte(s))
			return
		}
		p.pastBypass = true
	}
	for _, r := range s {
		p.buf = append(p.buf, r)
	}
	p.cond.Signal()
	p.mu.Unlock()
}

// Flush drains the queue synchronously, returning when out has
// received every rune queued before this call. Subsequent Push calls
// will queue again. Safe to call from any goroutine; safe to call
// multiple times. No-op when pacing is disabled.
func (p *Pacer) Flush() {
	if p.opts.TargetCPS <= 0 {
		return
	}
	// Park a "marker" by recording the queue length at this moment
	// and waiting until the drainer has consumed at least that many
	// runes. Since the drainer only ever shrinks the queue and Push
	// only ever appends, "queue length never grows above zero before
	// it dips to zero" is equivalent to "we caught up."
	p.mu.Lock()
	for len(p.buf) > 0 && !p.closed {
		// Nudge the drainer in case it's parked on cond.Wait, then
		// release the lock and let it run a tick. We re-acquire on
		// the next iteration.
		p.cond.Signal()
		p.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		p.mu.Lock()
	}
	p.mu.Unlock()
}

// Close stops the drainer and flushes anything still queued. Safe to
// call multiple times. Must eventually be called or the drainer
// goroutine leaks.
func (p *Pacer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	if p.opts.TargetCPS > 0 {
		close(p.stopCh)
		<-p.drainedCh
	}
}

// run is the drainer goroutine. It wakes on a ticker (target CPS
// pacing) or on Push notifications, emitting runes from the buffer
// to out. Adaptive speedup: when the buffer is large, emit more
// runes per tick.
func (p *Pacer) run() {
	defer close(p.drainedCh)
	// One rune per tick at target rate. We always emit at least one
	// rune per wake when the buffer is non-empty, so the apparent
	// lower bound on tick interval governs the visible speed.
	interval := time.Second / time.Duration(p.opts.TargetCPS)
	if interval < time.Millisecond {
		interval = time.Millisecond // sanity floor
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			// Final drain — emit everything left, ignoring pacing.
			p.mu.Lock()
			rest := p.buf
			p.buf = nil
			p.mu.Unlock()
			if len(rest) > 0 {
				p.out.Write([]byte(string(rest)))
			}
			return
		case <-ticker.C:
		}

		p.mu.Lock()
		if len(p.buf) == 0 {
			// Park until either Push wakes us or Close stops us. We
			// also re-check the ticker after waking so that an empty
			// queue followed by a single Push doesn't burst-flush
			// past the rate cap.
			p.cond.Wait()
			if p.closed {
				rest := p.buf
				p.buf = nil
				p.mu.Unlock()
				if len(rest) > 0 {
					p.out.Write([]byte(string(rest)))
				}
				return
			}
			p.mu.Unlock()
			continue
		}

		// Adaptive speedup: emit more runes per tick when the queue
		// is large. Buckets keep the math simple and predictable.
		// At MaxLagRunes the rate doubles; at 4×MaxLagRunes it goes
		// up by ~10×. Beyond that we drain in big gulps to catch up.
		n := 1
		switch {
		case len(p.buf) > p.opts.MaxLagRunes*4:
			n = len(p.buf) / 8
		case len(p.buf) > p.opts.MaxLagRunes*2:
			n = 4
		case len(p.buf) > p.opts.MaxLagRunes:
			n = 2
		}
		if n > len(p.buf) {
			n = len(p.buf)
		}
		emit := make([]rune, n)
		copy(emit, p.buf[:n])
		p.buf = p.buf[n:]
		p.mu.Unlock()

		p.out.Write([]byte(string(emit)))
	}
}
