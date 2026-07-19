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

func TestReadPage(t *testing.T) {
	c := newCachedLog[uint64](buildLog(t, []uint64{10, 20, 30, 40, 50}))

	page, total := c.ReadPage(20, 0, 2)
	assert.Equal(t, 5, total)
	assert.Equal(t, []uint64{20, 30}, fks(page))

	page, total = c.ReadPage(0, 50, 2)
	assert.Equal(t, 5, total)
	assert.Equal(t, []uint64{30, 40}, fks(page))
}
