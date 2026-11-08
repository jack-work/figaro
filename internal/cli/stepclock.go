package cli

import (
	"container/heap"
	"sync"
	"time"
)

type stepClock struct {
	mu     sync.Mutex
	epoch  time.Time
	at     time.Duration
	seq    uint64
	timers stepTimers
}

type stepTimer struct {
	due   time.Duration
	seq   uint64
	every time.Duration
	f     func()
	index int
}

type stepTimers []*stepTimer

func (h stepTimers) Len() int { return len(h) }
func (h stepTimers) Less(i, j int) bool {
	if h[i].due != h[j].due {
		return h[i].due < h[j].due
	}
	return h[i].seq < h[j].seq
}
func (h stepTimers) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *stepTimers) Push(x any) {
	t := x.(*stepTimer)
	t.index = len(*h)
	*h = append(*h, t)
}
func (h *stepTimers) Pop() any {
	old := *h
	t := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	t.index = -1
	return t
}

func newStepClock(epoch time.Time) *stepClock { return &stepClock{epoch: epoch} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch.Add(c.at)
}

func (c *stepClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *stepClock) AfterFunc(d time.Duration, f func()) func() bool {
	if d < 0 {
		d = 0
	}
	return c.schedule(d, 0, f)
}

func (c *stepClock) Every(d time.Duration, f func()) func() {
	stop := c.schedule(d, d, f)
	return func() { stop() }
}

func (c *stepClock) schedule(d, every time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &stepTimer{due: c.at + d, seq: c.seq, every: every, f: f}
	heap.Push(&c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.index < 0 {
			return false
		}
		heap.Remove(&c.timers, t.index)
		return true
	}
}

func (c *stepClock) nextDue() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		return 0, false
	}
	return c.timers[0].due, true
}

func (c *stepClock) advance(to time.Duration) {
	c.mu.Lock()
	if to > c.at {
		c.at = to
	}
	c.mu.Unlock()
}

func (c *stepClock) fireOne(upTo time.Duration) bool {
	c.mu.Lock()
	if len(c.timers) == 0 || c.timers[0].due > upTo {
		c.mu.Unlock()
		return false
	}
	t := heap.Pop(&c.timers).(*stepTimer)
	if t.due > c.at {
		c.at = t.due
	}
	if t.every > 0 {
		c.seq++
		t.due += t.every
		t.seq = c.seq
		heap.Push(&c.timers, t)
	}
	f := t.f
	c.mu.Unlock()
	f()
	return true
}
