package causal_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/causal"
)

func TestSlice_ZeroValue(t *testing.T) {
	var s causal.Slice[int]
	assert.Equal(t, 0, s.Len())
	_, ok := s.Last()
	assert.False(t, ok)
	require.Panics(t, func() { _ = s.At(0) })
}

func TestSlice_Wrap(t *testing.T) {
	src := []int{1, 2, 3}
	s := causal.Wrap(src)
	assert.Equal(t, 3, s.Len())
	assert.Equal(t, 1, s.At(0))
	assert.Equal(t, 3, s.At(2))
	require.Panics(t, func() { _ = s.At(3) })
	require.Panics(t, func() { _ = s.At(-1) })
}

func TestSlice_Prefix(t *testing.T) {
	src := []int{1, 2, 3, 4, 5}

	mid := causal.Prefix(src, 3)
	assert.Equal(t, 3, mid.Len())
	assert.Equal(t, 3, mid.At(2))
	require.Panics(t, func() { _ = mid.At(3) }, "must not see past cursor")

	zero := causal.Prefix(src, 0)
	assert.Equal(t, 0, zero.Len())

	full := causal.Prefix(src, len(src))
	assert.Equal(t, 5, full.Len())

	require.Panics(t, func() { causal.Prefix(src, -1) })
	require.Panics(t, func() { causal.Prefix(src, 6) })
}

func TestSlice_Materialize_IsCopy(t *testing.T) {
	src := []int{1, 2, 3}
	s := causal.Wrap(src)
	got := s.Materialize()
	require.Equal(t, []int{1, 2, 3}, got)

	got[0] = 99
	assert.Equal(t, 1, src[0], "Materialize must copy; mutating result must not affect backing")
	assert.Equal(t, 1, s.At(0))
}

func TestSlice_Last(t *testing.T) {
	v, ok := causal.Wrap([]string{"a", "b", "c"}).Last()
	assert.True(t, ok)
	assert.Equal(t, "c", v)

	_, ok = causal.Wrap[string](nil).Last()
	assert.False(t, ok)
}

func TestSlice_Wrap_FrozenAtCallTime(t *testing.T) {
	// Wrap captures len() at call time. Subsequent appends to the
	// underlying slice are NOT visible through the captured Slice —
	// that's the contract that makes the prefix view stable.
	src := []int{1, 2}
	s := causal.Wrap(src)
	require.Equal(t, 2, s.Len())

	src = append(src, 3) // pretend the producer kept writing
	_ = src              // appease the linter
	assert.Equal(t, 2, s.Len(), "Wrap captures cursor at call time")
}

func TestSink_AppendAndSlice(t *testing.T) {
	sink := causal.NewSink[int]()
	assert.Equal(t, 0, sink.Len())

	idx := sink.Append(10)
	assert.Equal(t, 0, idx)
	idx = sink.Append(20)
	assert.Equal(t, 1, idx)

	assert.Equal(t, 2, sink.Len())

	view := sink.Slice()
	assert.Equal(t, 2, view.Len())
	assert.Equal(t, 10, view.At(0))
	assert.Equal(t, 20, view.At(1))
}

func TestSink_SliceIsStable_AcrossAppends(t *testing.T) {
	sink := causal.NewSink[int]()
	sink.Append(1)
	sink.Append(2)
	v1 := sink.Slice() // captures cursor=2

	sink.Append(3)
	sink.Append(4)
	v2 := sink.Slice() // captures cursor=4

	assert.Equal(t, 2, v1.Len(), "earlier Slice view stays stable past further appends")
	assert.Equal(t, 4, v2.Len())
}
