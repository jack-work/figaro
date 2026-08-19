package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildLog seeds a MemLog with entries at the given FigaroLTs (sparse allowed).
// Each entry's payload is its FigaroLT so tests can assert order easily.
func buildLog(t testing.TB, fks []uint64) *MemLog[uint64] {
	t.Helper()
	s := NewMemLog[uint64]()
	for _, fk := range fks {
		_, err := s.Append(Entry[uint64]{FigaroLT: fk, Payload: fk})
		require.NoError(t, err)
	}
	return s
}

func fks(entries []Entry[uint64]) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.FigaroLT
	}
	return out
}

func TestReadPage(t *testing.T) {
	c := newCachedLog[uint64](buildLog(t, []uint64{10, 20, 30, 40, 50}))

	page, total := c.ReadPage(20, 0, 2)
	assert.Equal(t, 5, total)
	assert.Equal(t, []uint64{20, 30}, fks(page))

	page, total = c.ReadPage(0, 50, 2)
	assert.Equal(t, 5, total)
	assert.Equal(t, []uint64{30, 40}, fks(page))
}

// Read is point-in-time by construction: it copies. This used to be
// store.Snapshot, which handed back the cache's own backing slice: free, and
// exactly why full residency became something callers silently relied on.
func TestRead_CachedLogRemainsStableAfterAppend(t *testing.T) {
	inner := buildLog(t, []uint64{10, 20})
	c := newCachedLog[uint64](inner)
	before := c.Read()

	_, err := c.Append(Entry[uint64]{FigaroLT: 30, Payload: 30})
	assert.NoError(t, err)
	assert.Equal(t, []uint64{10, 20}, fks(before), "an earlier read saw a later append")
	assert.Equal(t, []uint64{10, 20, 30}, fks(c.Read()))
}

// TailAfter is the suffix read the translator uses. It must return the
// entries past the watermark AND the whole log's count, since the caller
// validates its watermark by comparing prefix length.
func TestTailAfter_SuffixAndTotal(t *testing.T) {
	inner := buildLog(t, []uint64{10, 20, 30})
	c := newCachedLog[uint64](inner)

	all, total := TailAfter[uint64](c, 0)
	assert.Equal(t, 3, total)
	assert.Len(t, all, 3, "watermark 0 must read the whole log")

	tail, total := TailAfter[uint64](c, all[0].LT)
	assert.Equal(t, 3, total)
	assert.Len(t, tail, 2, "suffix past the first entry")

	tail, total = TailAfter[uint64](c, all[2].LT)
	assert.Equal(t, 3, total)
	assert.Empty(t, tail, "nothing past the tail")

	// The fallback path (no TailAfter method) must agree exactly.
	fallback, ftotal := TailAfter[uint64](onlyLog[uint64]{c}, all[0].LT)
	assert.Equal(t, total, ftotal)
	assert.Equal(t, fks(tailOf(c, all[0].LT)), fks(fallback))
}

// onlyLog hides the optional interfaces so TailAfter takes its generic path.
type onlyLog[T any] struct{ Log[T] }

func tailOf[T any](c *cachedLog[T], lt uint64) []Entry[T] {
	out, _ := c.TailAfter(lt)
	return out
}

func TestTailSnapshot_CachedLogIsAscending(t *testing.T) {
	inner := buildLog(t, []uint64{10, 20, 30})
	c := newCachedLog[uint64](inner)
	assert.Equal(t, []uint64{20, 30}, fks(TailSnapshot[uint64](c, 2)))
}

// newCachedLog is an UNBOUNDED cache: everything decoded is retained. No
// shipped configuration builds one -- production always passes a window or a
// budget -- so the constructor lives with the tests that want a cache whose
// hit-rate is 100% by construction.
func newCachedLog[T any](inner Log[T]) *cachedLog[T] {
	return newWindowedLog[T](inner, 0, 0, 1, 1, nil)
}
