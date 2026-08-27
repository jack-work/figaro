package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// collect drains a scan into the slice Read would have built. TESTS ONLY: the
// point of Scan is that the send path never does this.
func collect[T any](log Log[T], from, to uint64) []Entry[T] {
	var out []Entry[T]
	Scan(log, from, to, func(e Entry[T]) bool {
		out = append(out, e)
		return true
	})
	return out
}

// Scan must answer exactly as Read, whatever residency did. Read is the
// oracle: the streamed walk is only allowed to change WHERE the entries are
// held, never which ones or in what order.
func TestScanIsReadWithoutTheArray(t *testing.T) {
	for _, budget := range []int64{0, 1 << 10, 1 << 20} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			mem := NewMemLog[string]()
			for i := 0; i < 200; i++ {
				_, err := mem.Append(Entry[string]{Payload: fmt.Sprintf("row-%03d", i)})
				require.NoError(t, err)
			}
			sizeOf := func(e Entry[string]) int { return len(e.Payload) + 48 }
			cache := NewIRCache[string](fwtree.NewBudget(budget), func(string) Log[string] { return mem }, sizeOf, irKey[string])
			l := newTreeLog[string](mem, "aria", cache, sizeOf, irKey[string], nil)

			// Warm SOME of it, so the walk crosses resident runs and holes in
			// the same pass: the case peek exists for.
			_ = l.ReadFrom(40, 30)
			_ = l.ReadFrom(120, 10)

			require.Equal(t, l.Read(), collect[string](l, 0, 0), "whole channel")

			for _, span := range [][2]uint64{{0, 10}, {35, 75}, {119, 131}, {150, 200}, {195, 400}} {
				want := make([]Entry[string], 0)
				for _, e := range l.Read() {
					if e.LT > span[0] && e.LT <= span[1] {
						want = append(want, e)
					}
				}
				got := collect[string](l, span[0], span[1])
				require.Equal(t, want, got, "Scan(%d,%d)", span[0], span[1])
			}
		})
	}
}

// The substrate's own scan is the same read under one open.
func TestXwalLogScanMatchesRead(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	defer b.Close()

	outfit, err := b.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := b.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	rows, err := b.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)
	for i := 0; i < 64; i++ {
		_, err := rows.Append(Entry[[]json.RawMessage]{
			FigaroLT: uint64(i + 1),
			Payload:  []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"role":"user","n":%d}`, i))},
		})
		require.NoError(t, err)
	}

	want := rows.Read()
	require.Len(t, want, 64)
	require.Equal(t, want, collect[[]json.RawMessage](rows, 0, 0))

	// A scan that stops early stops the read: nothing past the refusal is
	// decoded, which is what makes an early exit cheap.
	seen := 0
	Scan(rows, 0, 0, func(Entry[[]json.RawMessage]) bool {
		seen++
		return seen < 3
	})
	require.Equal(t, 3, seen)
}

// A walk bounded at a coordinate stops there even as the log grows: the
// property a replayed request body depends on.
func TestScanBoundHoldsAgainstAppends(t *testing.T) {
	mem := NewMemLog[string]()
	for i := 0; i < 10; i++ {
		_, err := mem.Append(Entry[string]{Payload: fmt.Sprintf("row-%d", i)})
		require.NoError(t, err)
	}
	before := collect[string](mem, 0, 10)
	require.Len(t, before, 10)

	_, err := mem.Append(Entry[string]{Payload: "row-10"})
	require.NoError(t, err)

	require.Equal(t, before, collect[string](mem, 0, 10), "a bounded walk does not see the append")
	require.Len(t, collect[string](mem, 0, 0), 11, "an unbounded one does")
}
