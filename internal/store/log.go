package store

import "sort"

// Entry is one record on a Log. LT/FigaroLT are stamped on append.
// Fingerprint detects encoder-config drift in translations.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
	// ChalkVersion, on IR entries only: how far the form had advanced
	// when this turn was written. The board is unkeyed, so a patch carries
	// no turn; this is the other side of that association, and it rides
	// along on a record the reader is already holding.
	ChalkVersion uint64
	// EncodedBytes is the record's on-disk payload size, captured at decode
	// because that is the one place it is known for free.
	//
	// It exists to size the cache. Estimating retained bytes from the decoded
	// struct means guessing at allocator rounding on every string, slice and
	// boxed map value — an attempt at that came out 3x low. The encoded size,
	// times a measured inflation factor, is both cheaper and closer: decoded
	// IR ran 4.0x and 5.3x its encoded bytes on two real arias.
	EncodedBytes int
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

// tailBudgetedLog reads the tail without decoding the prefix. Optional: an
// implementation that cannot do it gets the read-everything-then-compact path,
// which is correct and merely wasteful.
type tailBudgetedLog[T any] interface {
	TailBudgeted(budget, maxRows, inflation int) ([]Entry[T], int)
}

// tailAfterLog is the suffix read. Optional so an implementation without a
// cheap one falls back to the generic walk.
type tailAfterLog[T any] interface {
	TailAfter(lt uint64) ([]Entry[T], int)
}

// TailAfter returns the entries strictly after channel LT lt, ascending, plus
// the log's total entry count.
//
// It exists so an incremental consumer can read the suffix it needs WITHOUT
// materializing the prefix it does not. That distinction is the difference
// between a translator holding 12 MiB of decoded IR and holding the handful
// of messages it is about to encode: the fig IR is 4-5x its wire bytes, so
// the prefix is the single largest thing a live aria pins.
//
// The total is returned alongside because the caller needs both halves to
// validate its watermark: prefix length is total-len(suffix), and comparing
// that against the count it last saw proves the log has only been appended
// to. Returning them together makes that check one atomic read rather than
// two that can disagree.
func TailAfter[T any](log Log[T], lt uint64) ([]Entry[T], int) {
	if t, ok := log.(tailAfterLog[T]); ok {
		return t.TailAfter(lt)
	}
	all := log.Read()
	i := 0
	for i < len(all) && all[i].LT <= lt {
		i++
	}
	return all[i:], len(all)
}

type tailSnapshotLog[T any] interface {
	TailSnapshot(n int) []Entry[T]
}

// store.Snapshot is gone on purpose. It returned the cache's own backing
// slice when the log happened to be materialized, which made "the entire log
// is in RAM" free at the call site and therefore load-bearing everywhere.
// Both users wanted a suffix; they use TailAfter. A consumer that genuinely
// needs every entry calls Read and pays for the copy, which is the honest
// price once the prefix may not be resident.

// TailSnapshot returns a read-only ascending view of the last n entries.
func TailSnapshot[T any](log Log[T], n int) []Entry[T] {
	if s, ok := log.(tailSnapshotLog[T]); ok {
		return s.TailSnapshot(n)
	}
	entries := log.Read()
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
