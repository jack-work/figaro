package store

import "sort"

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
	Len() int
	// ReadFrom returns up to n entries whose FigaroLT is at least figaroLT,
	// in ascending order. n <= 0 returns every matching entry.
	ReadFrom(figaroLT uint64, n int) []Entry[T]
	// ReadPage returns a bounded page and the total entry count from one
	// snapshot. before takes precedence over from when non-zero.
	ReadPage(from, before uint64, n int) ([]Entry[T], int)
	Lookup(figaroLT uint64) (Entry[T], bool)
	PeekTail() (Entry[T], bool)

	// Append stamps e with a fresh LT and writes it to the log.
	Append(e Entry[T]) (Entry[T], error)

	Clear() error
}

type snapshotLog[T any] interface {
	Snapshot() []Entry[T]
}

type tailSnapshotLog[T any] interface {
	TailSnapshot(n int) []Entry[T]
}

// Snapshot returns a read-only, point-in-time view when the log is already
// materialized in memory, falling back to Read for other implementations.
func Snapshot[T any](log Log[T]) []Entry[T] {
	if s, ok := log.(snapshotLog[T]); ok {
		return s.Snapshot()
	}
	return log.Read()
}

// TailSnapshot returns a read-only ascending view of the last n entries.
func TailSnapshot[T any](log Log[T], n int) []Entry[T] {
	if s, ok := log.(tailSnapshotLog[T]); ok {
		return s.TailSnapshot(n)
	}
	entries := Snapshot(log)
	if n <= 0 || len(entries) == 0 {
		return nil
	}
	if n > len(entries) {
		n = len(entries)
	}
	return entries[len(entries)-n:]
}

func readPage[T any](rows []Entry[T], from, before uint64, n int) ([]Entry[T], int) {
	total := len(rows)
	if before > 0 {
		if n <= 0 {
			return nil, total
		}
		out := make([]Entry[T], 0, n)
		for i := len(rows) - 1; i >= 0 && len(out) < n; i-- {
			if rows[i].FigaroLT < before {
				out = append(out, rows[i])
			}
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out, total
	}
	start := 0
	if from > 0 {
		start = sort.Search(len(rows), func(i int) bool {
			return rows[i].FigaroLT >= from
		})
	}
	end := len(rows)
	if n > 0 && start+n < end {
		end = start + n
	}
	out := make([]Entry[T], end-start)
	copy(out, rows[start:end])
	return out, total
}
