package arialog

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/jack-work/figaro/figwal/segment"
	"github.com/jack-work/figaro/figwal/xwal"
)

type Entry[T any] struct {
	LT          uint64
	FigaroLT    uint64
	Payload     T
	Fingerprint string

	FormChannelVersion uint64

	StudyVersions map[string]uint64

	EncodedBytes int

	FigaroHash string
}

type Log[T any] interface {
	Read() []Entry[T]
	Len() int

	ReadFrom(figaroLT uint64, n int) []Entry[T]

	ReadPage(from, before uint64, n int) ([]Entry[T], int)
	Lookup(figaroLT uint64) (Entry[T], bool)
	PeekTail() (Entry[T], bool)

	Append(e Entry[T]) (Entry[T], error)

	Clear() error

	TruncateFrom(lt uint64) error
}

var ErrBelowForkBase = xwal.ErrBelowForkBase
var ErrViewHidesOwnRows = errors.New("store: the lineage view serves an ancestor where this node owns rows")

type batchLog[T any] interface {
	AppendBatch(rows []Entry[T]) ([]Entry[T], error)
}

func AppendBatch[T any](log Log[T], rows []Entry[T]) ([]Entry[T], error) {
	if b, ok := log.(batchLog[T]); ok {
		return b.AppendBatch(rows)
	}
	out := make([]Entry[T], 0, len(rows))
	for _, e := range rows {
		stamped, err := log.Append(e)
		if err != nil {
			return out, err
		}
		out = append(out, stamped)
	}
	return out, nil
}

type tailAfterLog[T any] interface {
	TailAfter(lt uint64) ([]Entry[T], int)
}

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

func ReadPage[T any](rows []Entry[T], from, before uint64, n int) ([]Entry[T], int) {
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

func FigaroHash[T any](payload T) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return segment.ValueHash(b)
}
