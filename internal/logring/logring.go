// Package logring keeps a bounded, in-memory tail of slog records so a
// running daemon can be asked what it has been saying, without anyone
// grepping a file.
package logring

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultCapacity is how many records a ring retains. Records are small
// (a header plus five inline attrs; larger ones spill to a slice), so this
// is a couple of hundred kilobytes - immaterial next to the segment cache,
// and reported by `figaro doctor mem` alongside it.
const DefaultCapacity = 512

// Keep decides whether a record is worth retaining. It runs on the logging
// goroutine, so it must be cheap and must not block.
type Keep func(slog.Record) bool

// AtLeast keeps records at or above a level. This is the default policy and
// the reason the ring is free in normal operation: nothing is copied until
// something goes wrong.
func AtLeast(level slog.Level) Keep {
	return func(r slog.Record) bool { return r.Level >= level }
}

// WithMessage keeps records whose message matches exactly, at any level. It is
// how a subsystem opts a specific INFO-level stream into retention - provider
// round-trips, say - without lowering the bar for everything else.
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

// Any keeps a record if any of the given policies would.
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

// Entry is one retained record, flattened for reading. Seq is monotonic across
// the ring's whole life, so a gap between the oldest retained Seq and the
// newest is exactly how many records retention dropped.
type Entry struct {
	Seq   uint64         `json:"seq"`
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Ring is a slog.Handler that forwards to inner and retains a bounded tail.
// The zero value is not usable; call New.
type Ring struct {
	inner  slog.Handler
	keep   Keep
	prefix []slog.Attr // accumulated by WithAttrs
	groups []string    // accumulated by WithGroup

	// The buffer is shared by every derived handler (WithAttrs/WithGroup
	// return a new Ring pointing at the same store), because a ring per
	// derived logger would retain nothing useful.
	store *store
}

type store struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	seq  uint64
}

// New wraps inner. keep decides what is retained; nil means AtLeast(WARN).
// capacity <= 0 means DefaultCapacity.
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

// Handle forwards first, then retains. Forwarding first means a panic in our
// own bookkeeping cannot cost the caller their log line.
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

// flatten copies the record's attributes out. This is the one allocation the
// ring costs, and it is mandatory: a Handler may not retain a Record, whose
// overflow attributes live in a slice it does not own.
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

// Recent returns retained records oldest first and newest last,
// filtered by match (nil keeps all) and limited to the newest n (n <= 0 for
// everything retained).
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

// Stats reports what the ring is holding, for `figaro doctor mem`. Every
// other cache in the daemon reports its footprint; this one should too.
func (r *Ring) Stats() (retained, capacity int, seq uint64) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf), cap(s.buf), s.seq
}
