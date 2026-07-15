package store

// Entry is one record on a Log. LT/FigaroLT are stamped on append.
// Fingerprint detects encoder-config drift in translations.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
}

// Log is one column of an aria's write-ahead log. Logs are
// append-only; dangling state at the tail is repaired with an
// interrupt sentinel, not by truncation. Clear is supported for
// translator caches that invalidate wholesale on fingerprint
// mismatch.
//
// Two backing implementations: MemLog (ephemeral) and xwalLog (figwal
// segments). Translator caches use the same Log interface; they are
// not independently fork-able — forks ride along with the IR log.
type Log[T any] interface {
	// TODO: Pass direction iota, ascending or descending.
	Read() []Entry[T]
	Lookup(figaroLT uint64) (Entry[T], bool)
	PeekTail() (Entry[T], bool)
	ScanFromEnd(n int) []Entry[T]

	// ReadBefore returns up to n entries whose FigaroLT is strictly less than
	// figaroLT, in ASCENDING FigaroLT order (the n entries immediately preceding
	// the cursor). Fewer than n (or none) if the log doesn't have them. A cursor
	// of 0 is treated as "before the beginning" => empty.
	ReadBefore(figaroLT uint64, n int) []Entry[T]

	// Append stamps e with a fresh LT and writes it to the log.
	Append(e Entry[T]) (Entry[T], error)

	Clear() error
}
