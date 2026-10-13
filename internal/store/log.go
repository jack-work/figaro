package store

import (
	"encoding/json"
	"sort"

	"github.com/jack-work/figaro/internal/store/segment"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// Entry is one record on a Log. LT/FigaroLT are stamped on append.
// Fingerprint detects encoder-config drift in translations.
type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string
	// FormChannelVersion, IR entries only: the form version this turn was written at.
	FormChannelVersion uint64
	// StudyVersions, IR entries of a studying aria: each observed form's
	// version at write, keyed by form id.
	StudyVersions map[string]uint64
	// EncodedBytes is captured at decode, the one place it is known for free.
	EncodedBytes int
	// FigaroHash, translator rows only: the content hash of the IR record this
	// row translates. An identity, not a checksum: FigaroLT is a position and
	// can be reissued, so a row written for a record that never landed would
	// otherwise be adopted by whatever lands there next.
	FigaroHash string
}

// Log is one column of an aria's write-ahead log; append-only.
type Log[T any] interface {
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
	// TruncateFrom drops the rows at or after channel LT lt. A fork's
	// inherited prefix is out of reach: ErrBelowForkBase.
	TruncateFrom(lt uint64) error
}

var ErrBelowForkBase = xwal.ErrBelowForkBase

// tailBudgetedLog reads the tail without decoding the prefix; optional, the
// fallback is read-everything-then-compact.
type tailBudgetedLog[T any] interface {
	TailBudgeted(budget, maxRows, num, denom int) ([]Entry[T], int)
}

// tailAfterLog is the optional suffix read.
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

// readPage pages in the channel's own coordinates (LT), not FigaroLT, which
// is a foreign key on every side channel.
func readPage[T any](rows []Entry[T], from, before uint64, n int) ([]Entry[T], int) {
	total := len(rows)
	if before > 0 {
		if n <= 0 {
			return nil, total
		}
		out := make([]Entry[T], 0, n)
		for i := len(rows) - 1; i >= 0 && len(out) < n; i-- {
			if rows[i].LT < before {
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
			return rows[i].LT >= from
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

// FigaroHash is the truncated SHA-256 of canonical JSON, the same function the
// segment codec stamps on every record: an identity of the value, not of one
// serialization.
func FigaroHash[T any](payload T) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return segment.ValueHash(b)
}
