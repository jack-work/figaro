package arialog

type rangeScanner[T any] interface {
	ScanRange(from, to uint64, yield func(Entry[T]) bool)
}

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

type verifyOncer interface{ VerifyOnce() bool }

func VerifyOnce[T any](log Log[T]) bool {
	if v, ok := log.(verifyOncer); ok {
		return v.VerifyOnce()
	}
	return true
}

type ownRowsHider interface{ HidesOwnRows() bool }

func ViewHidesOwnRows[T any](log Log[T]) bool {
	h, ok := log.(ownRowsHider)
	return ok && h.HidesOwnRows()
}
