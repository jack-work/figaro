package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// The tree-backed log must answer exactly as the log beneath it, whatever
// residency did: that is the whole contract of putting a cache in front of
// something. The oracle is the inner log itself.
func TestTreeLogAnswersLikeItsSubstrate(t *testing.T) {
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

			require.Equal(t, mem.Len(), l.Len())

			for _, from := range []uint64{1, 2, 57, 199, 200} {
				for _, n := range []int{1, 5, 64, 0} {
					want := mem.ReadFrom(from, n)
					got := l.ReadFrom(from, n)
					require.Equal(t, len(want), len(got), "ReadFrom(%d,%d)", from, n)
					for i := range want {
						require.Equal(t, want[i].Payload, got[i].Payload, "ReadFrom(%d,%d)[%d]", from, n, i)
					}
				}
			}

			for _, lt := range []uint64{1, 33, 200} {
				want, wok := mem.Lookup(lt)
				got, gok := l.Lookup(lt)
				require.Equal(t, wok, gok, "Lookup(%d)", lt)
				if wok {
					require.Equal(t, want.Payload, got.Payload)
				}
			}

			tailWant, totalWant := TailAfter[string](mem, 150)
			tailGot, totalGot := l.TailAfter(150)
			require.Equal(t, totalWant, totalGot)
			require.Equal(t, len(tailWant), len(tailGot))

			require.Equal(t, 8, len(l.TailSnapshot(8)))
		})
	}
}

// An append lands in the substrate AND is seeded into the cache, so the tail a
// writer just wrote is resident without a fetch.
func TestTreeLogSeedsWhatItWrites(t *testing.T) {
	mem := NewMemLog[string]()
	sizeOf := func(e Entry[string]) int { return len(e.Payload) + 48 }
	cache := NewIRCache[string](fwtree.NewBudget(0), func(string) Log[string] { return mem }, sizeOf, irKey[string])
	l := newTreeLog[string](mem, "aria", cache, sizeOf, irKey[string], nil).seedingTail()

	stamped, err := l.Append(Entry[string]{Payload: "first"})
	require.NoError(t, err)
	require.Equal(t, uint64(1), stamped.FigaroLT)

	before := l.cache.Recomposes()
	got, ok := l.Lookup(stamped.FigaroLT)
	require.True(t, ok)
	require.Equal(t, "first", got.Payload)
	require.Equal(t, before, l.cache.Recomposes(), "reading what was just written called the source")
}

// Residency is reported from the tree's index, and a bounded budget bounds it.
func TestTreeLogResidencyIsTheTreesIndex(t *testing.T) {
	mem := NewMemLog[string]()
	body := string(make([]byte, 512))
	for i := 0; i < 300; i++ {
		mem.Append(Entry[string]{Payload: body})
	}
	sizeOf := func(e Entry[string]) int { return len(e.Payload) + 48 }
	// The budget must exceed ONE RUN or nothing can stay: a run larger than the
	// whole budget is evicted the moment it is charged, which is the
	// run-granularity-in-bytes question in plans/tree-shaped-log.md. 300 rows
	// of ~560 B in runs of 64 is ~36 KB per run.
	budget := int64(128 << 10)
	cache := NewIRCache[string](fwtree.NewBudget(budget), func(string) Log[string] { return mem }, sizeOf, irKey[string])
	l := newTreeLog[string](mem, "aria", cache, sizeOf, irKey[string], nil)

	_ = l.ReadFrom(1, 0) // pull the whole channel through the window
	l.cache.Budget().Settle(2 * time.Second)
	require.LessOrEqual(t, int64(l.ResidentBytes()), budget,
		"the tree's budget did not bound the log's residency")
	require.NotZero(t, l.Resident(), "nothing stayed resident at all")
}

// STRUCTURAL FORK SHARING: a child reads its inherited prefix out of its
// ANCESTOR's node, so the bytes are shared by construction rather than donated
// once at open and guarded by a probe.
func TestTreeLogForkSharesThePrefixStructurally(t *testing.T) {
	parentMem := NewMemLog[string]()
	for i := 0; i < 40; i++ {
		parentMem.Append(Entry[string]{Payload: fmt.Sprintf("p-%02d", i)})
	}
	sizeOf := func(e Entry[string]) int { return len(e.Payload) + 48 }
	// One cache, two tenants: parent and child are nodes in the same tree.
	subs := map[string]Log[string]{"parent": parentMem}
	cache := NewIRCache[string](fwtree.NewBudget(0), func(node string) Log[string] { return subs[node] }, sizeOf, irKey[string])
	parent := newTreeLog[string](parentMem, "parent", cache, sizeOf, irKey[string], nil)
	require.Len(t, parent.ReadFrom(1, 0), 40)

	// The child's own log carries only its own records; everything below the
	// fork base belongs to the parent.
	childMem := NewMemLog[string]()
	for i := 0; i < 3; i++ {
		childMem.Append(Entry[string]{Payload: fmt.Sprintf("c-%02d", i)})
	}
	const base = 41 // the child's first record is LT 41
	child := &treeLog[string]{
		inner: childMem, node: "child", sizeOf: sizeOf,
		lineage: func() []fwtree.Ref {
			return []fwtree.Ref{{Node: "parent"}, {Node: "child", Base: base}}
		},
	}
	child.cache = cache
	subs["child"] = childMem

	// A read spanning the seam is served from BOTH nodes, and the prefix costs
	// the child nothing: it is the parent's runs.
	before := parent.cache.Recomposes()
	got := child.span(0, 40)
	require.Len(t, got, 40, "the child could not read its inherited prefix")
	require.Equal(t, "p-00", got[0].Payload)
	require.Equal(t, before, parent.cache.Recomposes(),
		"the child re-materialized a prefix its parent already holds")
}
