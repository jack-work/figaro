package store

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func replaySince(t *testing.T, base form.Snapshot, s FormSince) form.Snapshot {
	t.Helper()
	require.True(t, s.Delta)
	for _, p := range s.Patches {
		base = base.Apply(p.Patch)
	}
	return base
}

// A catch-up from any version still held equals the snapshot; anything the
// form cannot answer exactly is a full snapshot, never a short delta.
func TestFormSinceCatchesUpOrFallsBackWhole(t *testing.T) {
	f := formWithPatches(t, 40)
	head := f.Read()

	full := f.Since(0)
	require.False(t, full.Delta)
	require.Equal(t, head.Version, full.Version)
	require.True(t, full.Snapshot.Diff(head.Snapshot).IsIdentity())

	same := f.Since(head.Version)
	require.True(t, same.Delta)
	require.Empty(t, same.Patches)

	for _, since := range []uint64{1, 7, 20, 39} {
		base := form.Snapshot{}
		for _, p := range f.PatchesBetween(0, since) {
			base = base.Apply(p.Patch)
		}
		got := replaySince(t, base, f.Since(since))
		require.True(t, got.Diff(head.Snapshot).IsIdentity(), "since %d", since)
	}

	ahead := f.Since(head.Version + 5)
	require.False(t, ahead.Delta, "a client ahead of the form is from another lineage: reseed it")
}

func TestFormSinceBelowTheTrimReadsTheLogOrReseeds(t *testing.T) {
	SetPatchWindow(8)
	t.Cleanup(func() { SetPatchWindow(2048) })
	setPage(t, 32)

	f := formWithPatches(t, 400)
	head := f.Read()
	st := f.state.Load()
	require.Positive(t, st.trimmed, "nothing trimmed; raise the count")

	got := f.Since(st.trimmed + 1)
	require.True(t, got.Delta, "inside the window is a delta")

	below := f.Since(1)
	require.False(t, below.Delta, "a memory log paged past version 1 cannot answer it; a short delta would lie")
	require.True(t, below.Snapshot.Diff(head.Snapshot).IsIdentity())
}
