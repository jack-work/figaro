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

// A WALK MUST CROSS A FORK SEAM THE WAY A READ DOES.
//
// Every other scan test here builds a log with no lineage, so cuts() returns
// one coordinate and the whole ancestor path -- substrateScan against another
// node, interleaved with resident runs -- never runs. That is the riskiest
// code in the change and it was the least covered: this test is the one that
// executes it.
func TestScanCrossesAForkSeam(t *testing.T) {
	parentMem := NewMemLog[string]()
	for i := 0; i < 40; i++ {
		_, err := parentMem.Append(Entry[string]{Payload: fmt.Sprintf("p-%02d", i)})
		require.NoError(t, err)
	}
	sizeOf := func(e Entry[string]) int { return len(e.Payload) + 48 }
	subs := map[string]Log[string]{"parent": parentMem}
	open := func(node string) Log[string] { return subs[node] }

	for _, budget := range []int64{0, 1 << 20} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			cache := NewIRCache[string](fwtree.NewBudget(budget), open, sizeOf, irKey[string])
			childMem := NewMemLog[string]()
			for i := 0; i < 3; i++ {
				_, err := childMem.Append(Entry[string]{Payload: fmt.Sprintf("c-%02d", i)})
				require.NoError(t, err)
			}
			const base = 41
			child := &treeLog[string]{
				inner: childMem, node: "child", sizeOf: sizeOf, key: irKey[string],
				lineage: func() []fwtree.Ref {
					return []fwtree.Ref{{Node: "parent"}, {Node: "child", Base: base}}
				},
			}
			child.cache = cache
			child.openNode = open
			subs["child"] = childMem

			// Warm a run in the MIDDLE of the parent's half, so the walk has
			// to interleave: substrate, resident, substrate, then the seam.
			_ = child.span(10, 25)

			want := child.peek(0, 40)
			got := collect[string](child, 0, 40)
			require.Equal(t, want, got, "the walk and the read disagree below the seam")
			require.Len(t, got, 40)
			require.Equal(t, "p-00", got[0].Payload)
			require.Equal(t, "p-39", got[39].Payload)

			// And an early exit inside the ancestor's half stops there.
			seen := 0
			Scan[string](child, 0, 40, func(Entry[string]) bool {
				seen++
				return seen < 7
			})
			require.Equal(t, 7, seen)
		})
	}
}
