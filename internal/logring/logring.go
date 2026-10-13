// Package logring keeps a bounded in-memory tail of slog records.
package logring

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const DefaultCapacity = 512

// Keep runs on the logging goroutine: cheap, non-blocking.
type Keep func(slog.Record) bool

func AtLeast(level slog.Level) Keep {
	return func(r slog.Record) bool { return r.Level >= level }
}

// WithMessage keeps records whose message matches exactly, at any level.
func WithMessage(msgs ...string) Keep {
	set := make(map[string]struct{}, len(msgs))
	for _, m := range msgs {
		set[m] = struct{}{}
	}
	return func(r slog.Record) bool {
		_, ok := set[r.Message]
		return ok
	}
}

func Any(keeps ...Keep) Keep {
	return func(r slog.Record) bool {
		for _, k := range keeps {
			if k != nil && k(r) {
				return true
			}
		}
		return false
	}
}

// Seq is monotonic across the ring's life, so a gap between the oldest
// retained Seq and the newest is what retention dropped.
type Entry struct {
	Seq   uint64         `json:"seq"`
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Ring is a slog.Handler that forwards to inner and retains a bounded tail.
type Ring struct {
	inner  slog.Handler
	keep   Keep
	prefix []slog.Attr // accumulated by WithAttrs
	groups []string    // accumulated by WithGroup

	// Shared by every derived handler.
	store *store
}

type store struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	seq  uint64
}

// New: nil keep means AtLeast(WARN); capacity <= 0 means DefaultCapacity.
func New(inner slog.Handler, capacity int, keep Keep) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if keep == nil {
		keep = AtLeast(slog.LevelWarn)
	}
	return &Ring{
		inner: inner,
		keep:  keep,
		store: &store{buf: make([]Entry, 0, capacity)},
	}
}

func (r *Ring) Enabled(ctx context.Context, l slog.Level) bool {
	return r.inner.Enabled(ctx, l)
}

// Handle forwards first, so a panic in retention cannot cost the log line.
func (r *Ring) Handle(ctx context.Context, rec slog.Record) error {
	err := r.inner.Handle(ctx, rec)
	if r.keep(rec) {
		r.store.add(r.flatten(rec))
	}
	return err
}

func (r *Ring) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *r
	next.inner = r.inner.WithAttrs(attrs)
	next.prefix = append(append([]slog.Attr(nil), r.prefix...), attrs...)
	return &next
}

func (r *Ring) WithGroup(name string) slog.Handler {
	next := *r
	next.inner = r.inner.WithGroup(name)
	next.groups = append(append([]string(nil), r.groups...), name)
	return &next
}

// flatten copies the attributes out: a Handler may not retain a Record.
func (r *Ring) flatten(rec slog.Record) Entry {
	e := Entry{
		Time:  rec.Time,
		Level: rec.Level.String(),
		Msg:   rec.Message,
	}
	n := len(r.prefix) + rec.NumAttrs()
	if n > 0 {
		e.Attrs = make(map[string]any, n)
		for _, a := range r.prefix {
			e.Attrs[a.Key] = a.Value.Resolve().Any()
		}
		rec.Attrs(func(a slog.Attr) bool {
			e.Attrs[a.Key] = a.Value.Resolve().Any()
			return true
		})
	}
	return e
}

func (s *store) add(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cap(s.buf) == 0 {
		return
	}
	s.seq++
	e.Seq = s.seq
	if len(s.buf) < cap(s.buf) {
		s.buf = append(s.buf, e)
		return
	}
	s.buf[s.next] = e
	s.next = (s.next + 1) % cap(s.buf)
}

// Recent returns retained records oldest first, filtered by match (nil keeps
// all) and limited to the newest n (n <= 0 for all).
func (r *Ring) Recent(n int, match func(Entry) bool) []Entry {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) == 0 {
		return nil
	}
	ordered := make([]Entry, 0, len(s.buf))
	if len(s.buf) < cap(s.buf) {
		ordered = append(ordered, s.buf...)
	} else {
		ordered = append(ordered, s.buf[s.next:]...)
		ordered = append(ordered, s.buf[:s.next]...)
	}
	out := ordered[:0:0]
	for _, e := range ordered {
		if match == nil || match(e) {
			out = append(out, e)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func (r *Ring) Stats() (retained, capacity int, seq uint64) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf), cap(s.buf), s.seq
}
