package angelus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	reclaimBackoffMin = time.Minute
	reclaimBackoffMax = time.Hour
	reclaimPace       = 20 * time.Millisecond
)

var errNotYet = errors.New("not yet")

type reclaimFailure struct {
	attempts int
	next     time.Time
}

type reclaimer struct {
	do   func(id string) error
	now  func() time.Time
	pace time.Duration

	mu       sync.Mutex
	queued   map[string]bool
	inflight map[string]bool
	order    []string
	fails    map[string]reclaimFailure
	wake     chan struct{}
}

func newReclaimer(do func(id string) error) *reclaimer {
	return &reclaimer{
		do:       do,
		now:      time.Now,
		pace:     reclaimPace,
		queued:   map[string]bool{},
		inflight: map[string]bool{},
		fails:    map[string]reclaimFailure{},
		wake:     make(chan struct{}, 1),
	}
}

func (r *reclaimer) enqueue(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queued[id] || r.inflight[id] {
		return false
	}
	if f, ok := r.fails[id]; ok && r.now().Before(f.next) {
		return false
	}
	r.queued[id] = true
	r.order = append(r.order, id)
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return true
}

func (r *reclaimer) pop() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return "", false
	}
	id := r.order[0]
	r.order = r.order[1:]
	delete(r.queued, id)
	r.inflight[id] = true
	return id, true
}

func (r *reclaimer) settle(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inflight, id)
	if err == nil || errors.Is(err, errNotYet) {
		delete(r.fails, id)
		return
	}
	f := r.fails[id]
	f.attempts++
	wait := reclaimBackoffMin << min(f.attempts-1, 6)
	wait = min(wait, reclaimBackoffMax)
	f.next = r.now().Add(wait)
	r.fails[id] = f
	slog.Warn("reclaim failed, backing off", "node", id, "attempts", f.attempts, "retry_in", wait, "err", err)
}

func (r *reclaimer) failing() map[string]reclaimFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]reclaimFailure, len(r.fails))
	for id, f := range r.fails {
		out[id] = f
	}
	return out
}

func (r *reclaimer) drain(ctx context.Context) {
	for ctx.Err() == nil {
		id, ok := r.pop()
		if !ok {
			return
		}
		r.settle(id, r.do(id))
	}
}

func (r *reclaimer) run(ctx context.Context) {
	for {
		for ctx.Err() == nil {
			id, ok := r.pop()
			if !ok {
				break
			}
			r.settle(id, r.do(id))
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.pace):
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
	}
}

func (a *Angelus) reclaims() *reclaimer {
	a.reclaimOnce.Do(func() { a.reclaim = newReclaimer(a.reapOne) })
	return a.reclaim
}

type roleSeeder interface {
	SeedRoles(ctx context.Context, pace time.Duration)
}

const roleSeedPace = 2 * time.Millisecond

func (a *Angelus) seedRoles(ctx context.Context) {
	if s, ok := a.Backend.(roleSeeder); ok {
		go s.SeedRoles(ctx, roleSeedPace)
	}
}

func (r *reclaimer) quiet() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order) == 0 && len(r.inflight) == 0
}
