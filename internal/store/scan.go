package store

// Scan is the read that does not materialize.
//
// Read() answers with a slice, so a caller that only wants to WALK a channel
// pays for a copy of the whole thing: on the send path that is the entire
// translated conversation, decoded into the heap, per request, and thrown
// away when the request ends. Measured on a live daemon at 0.29.1: 357MB of a
// 461MB live heap was one in-flight send holding exactly that slice.
//
// The walk itself is not the cost and is not being removed -- eeec2bc0 ruled
// for it, "if a caller needs a record it reads it, and the read brings it
// back". What is removed is the ARRAY: an entry is decoded, handed to yield,
// and dropped, so a walk of N records costs one record of heap instead of N.
//
// yield returns false to stop early. The coordinates are the log's OWN key
// space -- channel LT -- and the bracket is half-open on the left, (from..to],
// matching every other span in this package. to == 0 means "to the tail".

// rangeScanner is the optional streaming read. An implementation that cannot
// do it falls back to Read, which is correct and merely wasteful -- the same
// bargain tailBudgetedLog and tailAfterLog make.
type rangeScanner[T any] interface {
	ScanRange(from, to uint64, yield func(Entry[T]) bool)
}

// Scan walks (from..to] in ascending order, handing each entry to yield.
//
// IT IS THE HOT-PATH READ. Prefer it to Read anywhere the result is consumed
// once and in order.
func Scan[T any](log Log[T], from, to uint64, yield func(Entry[T]) bool) {
	if log == nil {
		return
	}
	if s, ok := log.(rangeScanner[T]); ok {
		s.ScanRange(from, to, yield)
		return
	}
	for _, e := range log.Read() {
		k := e.LT
		if k <= from || (to > 0 && k > to) {
			continue
		}
		if !yield(e) {
			return
		}
	}
}

// ScanAll walks the whole channel. The common case, spelled out so a caller
// does not have to know that (0..0] means everything.
func ScanAll[T any](log Log[T], yield func(Entry[T]) bool) { Scan(log, 0, 0, yield) }

// verifyOnce is a flag a log carries for the length of its own life. The
// optional interface exists so a once-per-log check does not need a
// process-global map keyed by the handle: such a map never forgets, so it
// outlives the aria, pins the handle the backend evicted, and grows for as
// long as the daemon runs.
type verifyOncer interface{ VerifyOnce() bool }

// VerifyOnce reports whether this is the first caller to ask, for THIS log
// handle. A log that cannot remember answers true every time, which is the
// safe direction: the check runs again rather than being skipped.
func VerifyOnce[T any](log Log[T]) bool {
	if v, ok := log.(verifyOncer); ok {
		return v.VerifyOnce()
	}
	return true
}
