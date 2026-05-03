package store

// Entry is one record on a Stream — live or durable. LT and FigaroLT
// are zero for live entries (no LT allocated until durability), set
// for durable entries.
//
// Fingerprint is optional; populated for translation entries to
// detect encoder-config drift.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
}

type EventKind int

const (
	KindLive EventKind = iota
	KindDurable
)

// Stream is one column of the per-aria multi-column log. Pure data
// store: Append/Condense/Lookup, no pub/sub. The bus (Inbox) is the
// only place subscribers fire.
type Stream[T any] interface {
	Durable() []Entry[T]
	Lookup(figaroLT uint64) (Entry[T], bool)
	PeekTail() (Entry[T], bool)
	ScanFromEnd(n int) []Entry[T]
	Live() []Entry[T]

	// Append routes e to the live tail (durable=false) or directly to
	// the durable head (durable=true).
	Append(e Entry[T], durable bool) (Entry[T], error)

	// Condense closes the live tail and writes one new durable entry
	// stamped with a fresh LT.
	Condense(e Entry[T]) (Entry[T], error)

	// DiscardLive clears the live tail without producing a durable
	// entry.
	DiscardLive() error

	Clear() error
	Close() error
}
