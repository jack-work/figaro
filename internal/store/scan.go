package store

// rangeScanner is the optional streaming read; without it Scan falls back to Read.
type rangeScanner[T any] interface {
	ScanRange(from, to uint64, yield func(Entry[T]) bool)
}

// Scan walks (from..to] in channel-LT order, one decoded entry at a time, and
// never materializes the walk. to == 0 means to the tail. Prefer it to Read
// wherever the result is consumed once and in order.
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

func ScanAll[T any](log Log[T], yield func(Entry[T]) bool) { Scan(log, 0, 0, yield) }

// verifyOncer keeps a once-per-log flag on the handle rather than in a
// process-global map, which would outlive the aria and pin evicted handles.
type verifyOncer interface{ VerifyOnce() bool }

// VerifyOnce reports whether this is the first ask for this handle. A log that
// cannot remember answers true: the check reruns rather than being skipped.
func VerifyOnce[T any](log Log[T]) bool {
	if v, ok := log.(verifyOncer); ok {
		return v.VerifyOnce()
	}
	return true
}
