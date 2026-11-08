package methods

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

const (
	RestAfter = 3
	FirstRest = time.Minute
	LongRest  = 30 * time.Minute
	errBytes  = 240
)

var ErrResting = errors.New("decision route resting")

type RestingError struct {
	Route string
	Until time.Time
}

func (e *RestingError) Error() string {
	return fmt.Sprintf("%s: %s until %s", ErrResting, e.Route, e.Until.Format("15:04:05"))
}

func (e *RestingError) Unwrap() error { return ErrResting }

type Breaker struct {
	mu     sync.Mutex
	now    func() time.Time
	routes map[string]*route
}

type route struct {
	failures int
	since    time.Time
	until    time.Time
	rest     time.Duration
	probing  bool
	last     string
}

func NewBreaker() *Breaker {
	return &Breaker{now: time.Now, routes: map[string]*route{}}
}

func (b *Breaker) Guard(a Asker) Asker {
	if b == nil || a == nil {
		return a
	}
	return guarded{b: b, a: a}
}

type guarded struct {
	b *Breaker
	a Asker
}

func (g guarded) Ask(ctx context.Context, src decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	name, err := src.Route()
	if err != nil {
		return g.a.Ask(ctx, src, q)
	}
	if err := g.b.admit(name); err != nil {
		return rpc.DecideResponse{}, err
	}
	resp, err := g.a.Ask(ctx, src, q)
	g.b.record(name, err)
	return resp, err
}

func (b *Breaker) admit(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.routes[name]
	if r == nil || r.failures < RestAfter {
		return nil
	}
	if r.probing || b.now().Before(r.until) {
		return &RestingError{Route: name, Until: r.until}
	}
	r.probing = true
	return nil
}

func countsAgainst(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(err, decide.ErrUpstream) || errors.Is(err, context.DeadlineExceeded)
}

func (b *Breaker) record(name string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.routes[name]
	switch {
	case err == nil:
		if r == nil {
			return
		}
		if r.failures >= RestAfter {
			figOtel.AddRestingRoutes(context.Background(), -1, attribute.String("route", name))
			slog.Info("decision route answering again", "route", name,
				"failures", r.failures, "rested", b.now().Sub(r.since).Round(time.Second))
		}
		delete(b.routes, name)
	case countsAgainst(err):
		if r == nil {
			r = &route{since: b.now()}
			b.routes[name] = r
		}
		r.failures++
		r.last = clipErr(err.Error())
		r.probing = false
		switch {
		case r.failures == RestAfter:
			r.rest = FirstRest
			r.until = b.now().Add(r.rest)
			figOtel.AddRestingRoutes(context.Background(), 1, attribute.String("route", name))
			slog.Warn("decision route resting: methods write their defaults without asking", "route", name,
				"failures", r.failures, "until", r.until, "err", r.last)
		case r.failures > RestAfter:
			r.rest = min(2*r.rest, LongRest)
			r.until = b.now().Add(r.rest)
		}
	default:
		if r != nil {
			r.probing = false
		}
	}
}

func clipErr(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= errBytes {
		return s
	}
	cut := errBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func (b *Breaker) Resting() []rpc.DecideRoute {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []rpc.DecideRoute
	for name, r := range b.routes {
		if r.failures < RestAfter {
			continue
		}
		out = append(out, rpc.DecideRoute{
			Route: name, Failures: r.failures,
			SinceMS: r.since.UnixMilli(), NextTryMS: r.until.UnixMilli(),
			LastError: r.last,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Route < out[j].Route })
	return out
}
