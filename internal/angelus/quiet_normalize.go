package angelus

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

type quietNormalizer interface {
	NormalizePending(skip func(id string) bool) bool
	NormalizeSkipping(ctx context.Context, skip func(id string) bool) (store.NormalizeReport, error)
}

type detachFailure struct {
	fails int
	until time.Time
}

type detachBackoff map[string]detachFailure

func (b detachBackoff) waiting(now time.Time) func(id string) bool {
	return func(id string) bool { return now.Before(b[id].until) }
}

func (b detachBackoff) settle(r store.NormalizeReport, now time.Time) {
	for _, id := range r.Detached {
		delete(b, id)
	}
	for _, id := range r.Failed {
		f := b[id]
		f.fails++
		f.until = now.Add(min(quietFailBackoff<<min(f.fails-1, 16), quietFailBackoffMax))
		b[id] = f
		slog.Warn("quiet normalize: a figaro would not detach", "id", id, "fails", f.fails, "retry_after", f.until.Sub(now).Round(time.Second))
	}
}

type activity struct {
	calls  atomic.Int64
	last   atomic.Int64
	wake   chan struct{}
	cancel atomic.Pointer[context.CancelFunc]
	failed detachBackoff
}

func isWork(method string) bool {
	if method == rpc.MethodStatus {
		return false
	}
	switch v, _ := rpc.VerbOf(method); v {
	case rpc.VerbRead, rpc.VerbListen, rpc.VerbNone:
		return false
	}
	return true
}

func (a *activity) enter() {
	a.calls.Add(1)
	a.last.Store(time.Now().UnixNano())
	if c := a.cancel.Load(); c != nil {
		(*c)()
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *activity) leave() {
	a.last.Store(time.Now().UnixNano())
	a.calls.Add(-1)
}

func (a *activity) quietIn(quiet time.Duration, now time.Time) time.Duration {
	if a.calls.Load() > 0 {
		return quiet
	}
	return time.Unix(0, a.last.Load()).Add(quiet).Sub(now)
}

func (a *Angelus) turnInFlight() bool {
	for _, info := range a.Registry.List() {
		if info.State == figaro.StateActive {
			return true
		}
	}
	return false
}

const (
	quietTurnCheck      = time.Second
	quietIdleRecheck    = 10 * time.Minute
	quietFailBackoff    = 10 * time.Minute
	quietFailBackoffMax = 24 * time.Hour
)

func (a *Angelus) normalizeWhenQuiet(ctx context.Context) {
	n, ok := a.Backend.(quietNormalizer)
	quiet := a.Settings.NormalizeAfterQuiet()
	if !ok || quiet <= 0 {
		return
	}
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if wait := a.activity.quietIn(quiet, time.Now()); wait > 0 {
			timer.Reset(wait)
			continue
		}
		if a.turnInFlight() {
			timer.Reset(quiet)
			continue
		}
		if !n.NormalizePending(a.activity.failed.waiting(time.Now())) {
			select {
			case <-ctx.Done():
				return
			case <-a.activity.wake:
			case <-time.After(quietIdleRecheck):
			}
			timer.Reset(quiet)
			continue
		}
		a.normalizeQuietly(ctx, n, quiet)
		timer.Reset(quiet)
	}
}

func (a *Angelus) normalizeQuietly(ctx context.Context, n quietNormalizer, quiet time.Duration) {
	pass, cancel := context.WithCancel(ctx)
	defer cancel()
	a.activity.cancel.Store(&cancel)
	defer a.activity.cancel.Store(nil)
	if a.activity.quietIn(quiet, time.Now()) > 0 {
		return
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(quietTurnCheck)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-pass.Done():
				return
			case <-tick.C:
				if a.turnInFlight() {
					cancel()
					return
				}
			}
		}
	}()
	slog.Info("quiet normalize begins")
	start := time.Now()
	if a.activity.failed == nil {
		a.activity.failed = detachBackoff{}
	}
	r, err := n.NormalizeSkipping(pass, a.activity.failed.waiting(start))
	a.activity.failed.settle(r, time.Now())
	took := time.Since(start).Round(time.Millisecond)
	switch {
	case errors.Is(err, context.Canceled):
		slog.Info("quiet normalize yielded to work", "detached", len(r.Detached), "failed", len(r.Failed), "remaining", len(r.Remaining), "took", took)
	case err != nil:
		slog.Warn("quiet normalize", "detached", len(r.Detached), "failed", len(r.Failed), "dropped", len(r.Dropped), "took", took, "err", err)
	default:
		slog.Info("quiet normalize", "detached", len(r.Detached), "dropped", len(r.Dropped), "took", took)
	}
}
