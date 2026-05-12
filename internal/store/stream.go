package store

// Entry is one record on a Stream. LT/FigaroLT are stamped on
// append. Fingerprint detects encoder-config drift in translations.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
}

// Stream is one column of the per-aria log. Streams are append-only;
// dangling state at the tail is repaired with an interrupt sentinel,
// not by truncation. Clear is supported for translator caches that
// invalidate wholesale on fingerprint mismatch.
type Stream[T any] interface {
	// TODO: Pass direction iota, ascending or descending.
	Read() []Entry[T]
	Lookup(figaroLT uint64) (Entry[T], bool)
	PeekTail() (Entry[T], bool)
	ScanFromEnd(n int) []Entry[T]

	// Append stamps e with a fresh LT and writes it to the stream.
	Append(e Entry[T]) (Entry[T], error)

	Clear() error
	Close() error
}
