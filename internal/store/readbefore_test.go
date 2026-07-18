package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildLog seeds a MemLog with entries at the given FigaroLTs (sparse allowed).
// Each entry's payload is its FigaroLT so tests can assert order easily.
func buildLog(t *testing.T, fks []uint64) *MemLog[uint64] {
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

func TestReadBefore(t *testing.T) {
	seed := []uint64{10, 20, 30, 40, 50}

	cases := []struct {
		name   string
		cursor uint64
		n      int
		want   []uint64
	}{
		{"middle", 40, 2, []uint64{20, 30}},
		{"middle-exact-boundary", 30, 5, []uint64{10, 20}},
		{"at-first", 10, 3, nil},
		{"below-first", 5, 3, nil},
		{"above-last", 999, 3, []uint64{30, 40, 50}},
		{"n-larger-than-avail", 100, 100, []uint64{10, 20, 30, 40, 50}},
		{"n-zero", 40, 0, nil},
		{"cursor-zero", 0, 3, nil},
		{"one-below-only", 15, 4, []uint64{10}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := buildLog(t, seed)
			got := fks(s.ReadBefore(tc.cursor, tc.n))
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
			// ascending order sanity
			for i := 1; i < len(got); i++ {
				assert.Less(t, got[i-1], got[i], "must be ascending")
			}
		})
	}
}

func TestReadBefore_Empty(t *testing.T) {
	s := NewMemLog[uint64]()
	assert.Empty(t, s.ReadBefore(100, 10))
	assert.Empty(t, s.ReadBefore(0, 10))
}

func TestReadBefore_CachedLog(t *testing.T) {
	inner := buildLog(t, []uint64{10, 20, 30, 40, 50})
	c := newCachedLog[uint64](inner)

	got := fks(c.ReadBefore(40, 2))
	assert.Equal(t, []uint64{20, 30}, got)

	assert.Empty(t, c.ReadBefore(10, 5))
	assert.Empty(t, c.ReadBefore(0, 5))
	assert.Equal(t, []uint64{30, 40, 50}, fks(c.ReadBefore(999, 3)))

	// appends should be visible through cache
	_, err := c.Append(Entry[uint64]{FigaroLT: 60, Payload: 60})
	require.NoError(t, err)
	assert.Equal(t, []uint64{40, 50}, fks(c.ReadBefore(60, 2)))
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
