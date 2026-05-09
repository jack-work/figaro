package store

// Entry is one record on a Stream. LT and FigaroLT are set when the
// entry is stamped on append.
//
// Fingerprint is optional; populated for translation entries to
// detect encoder-config drift.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
}

// Stream is one column of the per-aria multi-column log. Pure data
// store: Append/Read/Lookup, no pub/sub. The bus (Inbox) is the
// only place subscribers fire.
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
