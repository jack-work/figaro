// Package pacer smooths bursty SSE chunks into a steady character
// stream. Push is non-blocking; a background goroutine drains the
// queue at the target CPS. First-byte bypass preserves TTFT.
// Adaptive speedup kicks in when the queue grows past MaxLagRunes.
//
// Push is goroutine-safe. Close must be called to avoid leaks.
package pacer

import (
	"io"
	"sync"
	"time"
)

// Options configures a Pacer.
type Options struct {
	// TargetCPS is the emission rate in chars/sec. 0 disables pacing.
	TargetCPS int

	// MaxLagRunes is a soft cap before adaptive speedup. 0 = auto.
	MaxLagRunes int

	// FirstByteBypass is the sync-write window preserving TTFT.
	FirstByteBypass time.Duration
}

// Pacer drains queued runes into out at a target rate.
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

// New creates and starts a Pacer. Call Close when done.
func New(out io.Writer, opts Options) *Pacer {
	if opts.MaxLagRunes <= 0 && opts.TargetCPS > 0 {
		// ~1 second of runes at target rate before speedup.
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
		// Disabled: mark drained immediately.
		close(p.drainedCh)
	}
	return p
}

// Push enqueues s for paced emission. Writes synchronously if
// pacing is disabled.
func (p *Pacer) Push(s string) {
	if s == "" {
		return
	}
	if p.opts.TargetCPS <= 0 {
		p.out.Write([]byte(s))
		return
	}
	// First-byte bypass: sync-write while in the bypass window
	// with empty queue to preserve TTFT.
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

// Flush drains the queue synchronously. No-op when disabled.
func (p *Pacer) Flush() {
	if p.opts.TargetCPS <= 0 {
		return
	}
	// Spin until the drainer has emptied the queue.
	p.mu.Lock()
	for len(p.buf) > 0 && !p.closed {
		// Nudge the drainer, release lock, let it run.
		p.cond.Signal()
		p.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		p.mu.Lock()
	}
	p.mu.Unlock()
}

// Close stops the drainer and flushes remaining runes.
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

// run is the drainer goroutine.
func (p *Pacer) run() {
	defer close(p.drainedCh)
	// One rune per tick at target rate.
	interval := time.Second / time.Duration(p.opts.TargetCPS)
	if interval < time.Millisecond {
		interval = time.Millisecond // sanity floor
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			// Final drain.
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
			// Park until Push or Close.
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

		// Adaptive speedup when queue is large.
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
