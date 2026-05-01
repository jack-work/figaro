// Package causal provides typed read-only views over a prefix of a
// slice — a "causal mask" that gives downstream code visibility into
// the past without the ability to peek at the future.
//
// The motivating use case is the figaro→native and native→figaro
// translators in the provider layer. When a translator computes the
// wire-format projection of figaro message i, it should be able to
// read figaro[0..i] and the native messages it has already emitted in
// this regeneration pass — but never figaro[i+1..]. The Slice type
// makes that constraint structural rather than convention: there is
// no method that returns indices ≥ Len, and the underlying slice is
// returned only as a copy via Materialize.
//
// Slice is a value type wrapping a backing slice and a cursor. The
// zero value (Slice[T]{}) is an empty mask — Len()==0, At() panics —
// and is safe to use without initialization.
//
// Producers (the agent, persistence layer) write through CausalSink:
// an append-only handle paired with a Slice that grows as new
// entries land. The translator never sees the sink, only the slice.
//
// See plans/aria-storage/log-unification.md (Stage D.2b) for the
// design rationale.
package causal

// Slice is a typed prefix view over a slice. Reads in [0..Len) are
// valid; reads past Len panic. The backing slice may grow beyond Len
// in the producer's hand without the consumer noticing — that's the
// point.
type Slice[T any] struct {
	backing []T
	cursor  int
}

// Wrap returns a Slice exposing the entire current contents of s.
// Future appends to s are NOT visible through the returned Slice
// unless the caller obtains a fresh Wrap. Used at the boundary
// where a producer hands off to a consumer.
func Wrap[T any](s []T) Slice[T] {
	return Slice[T]{backing: s, cursor: len(s)}
}

// Prefix returns a Slice exposing s[0..n]. Panics if n is negative
// or greater than len(s). Used to construct the prefix mask passed
// to translators that should only see up to position n.
func Prefix[T any](s []T, n int) Slice[T] {
	if n < 0 || n > len(s) {
		panic("causal.Prefix: index out of range")
	}
	return Slice[T]{backing: s, cursor: n}
}

// Len reports the number of visible entries.
func (c Slice[T]) Len() int { return c.cursor }

// At returns the entry at index i. Panics if i is outside [0, Len).
func (c Slice[T]) At(i int) T {
	if i < 0 || i >= c.cursor {
		panic("causal.Slice.At: index out of range")
	}
	return c.backing[i]
}

// Materialize returns a fresh slice containing the visible entries.
// Mutations to the returned slice do not affect the underlying
// backing. Use this when the consumer needs to pass the prefix to
// code that wants a plain []T.
func (c Slice[T]) Materialize() []T {
	out := make([]T, c.cursor)
	copy(out, c.backing[:c.cursor])
	return out
}

// Last returns the entry at index Len-1 and ok=true; ok=false when
// the slice is empty. Convenience for translators that frequently
// consult the most recently committed entry.
func (c Slice[T]) Last() (T, bool) {
	var zero T
	if c.cursor == 0 {
		return zero, false
	}
	return c.backing[c.cursor-1], true
}

// Sink is an append-only producer handle paired with a Slice. The
// Slice() method returns a Slice exposing entries committed so far;
// each Append() advances both the backing array and the cursor that
// future Slice() calls will see.
//
// Sink is not safe for concurrent use; the actor model assumes a
// single producer goroutine.
type Sink[T any] struct {
	backing []T
}

// NewSink returns a fresh Sink with no entries.
func NewSink[T any]() *Sink[T] {
	return &Sink[T]{}
}

// Append adds an entry. Returns the index it was assigned (Len-1
// after the append).
func (s *Sink[T]) Append(t T) int {
	s.backing = append(s.backing, t)
	return len(s.backing) - 1
}

// Slice returns a Slice exposing all entries appended to date.
// Subsequent Append calls do NOT change what the returned Slice
// shows — that's intentional: a translator that captured a Slice
// keeps a stable view for the duration of its computation.
func (s *Sink[T]) Slice() Slice[T] {
	return Slice[T]{backing: s.backing, cursor: len(s.backing)}
}

// Len reports the number of appended entries (independent of any
// outstanding Slice views).
func (s *Sink[T]) Len() int { return len(s.backing) }
