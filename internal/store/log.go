package store

import (
	"encoding/json"
	"sort"

	"github.com/jack-work/figaro/internal/store/segment"
)

// Entry is one record on a Log. LT/FigaroLT are stamped on append.
// Fingerprint detects encoder-config drift in translations.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
	// FormChannelVersion, on IR entries only: how far the form had advanced
	// when this turn was written. The board is unkeyed, so a patch carries
	FormChannelVersion uint64
	// StudyVersions, on IR entries of a STUDYING aria: where each observed
	// form stood when this record was written, keyed by form id: the
	StudyVersions map[string]uint64
	// EncodedBytes is the record's on-disk payload size, captured at decode
	// because that is the one place it is known for free.
	EncodedBytes int
	// FigaroHash, on TRANSLATOR rows only: the content hash of the fig IR
	// record this row translates.
	//
	// IT IS AN IDENTITY, NOT A CHECKSUM. A row names its record by FigaroLT,
	// which is a POSITION, and a position can be reissued -- the next main LT
	// is derived from what is durable, not reserved, so a row written for a
	// record that never landed would be adopted by whatever lands there next.
	// The row would then read as a legitimate translation of a message that
	// was never in the conversation. Hashing the CONTENT makes that
	// detectable: the row either describes the record at that position or it
	// does not.
	FigaroHash string
}

// Log is one column of an aria's write-ahead log. Logs are
// append-only; dangling state at the tail is repaired with an
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
	TailBudgeted(budget, maxRows, num, denom int) ([]Entry[T], int)
}

// tailAfterLog is the suffix read. Optional so an implementation without a
// cheap one falls back to the generic walk.
type tailAfterLog[T any] interface {
	TailAfter(lt uint64) ([]Entry[T], int)
}

// TailAfter returns the entries strictly after channel LT lt, ascending, plus
// the log's total entry count.
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

// FigaroHash is the content identity of a fig IR record: the truncated
// SHA-256 of its canonical JSON, which is the same function the segment codec
// already stamps on every record it writes. Two records that differ only in
// key order or whitespace hash alike, which is what makes it an identity of
// the VALUE rather than of a particular serialization.
func FigaroHash[T any](payload T) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return segment.ValueHash(b)
}
