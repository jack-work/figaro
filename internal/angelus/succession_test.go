package angelus

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A restart must find every in-flight succession from the one file, without
// reading any aria.
func TestSuccessionsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "successions.json")
	s := newSuccessions(path)
	s.load()
	require.NoError(t, s.put(successionRecord{Old: "aaaa", Phase: successionHandoff, DeadlineMS: 7}))
	require.NoError(t, s.put(successionRecord{Old: "bbbb", Phase: successionRamping, Successor: "cccc", Roles: []string{"@r"}, DeadlineMS: 9, Purge: true}))
	require.NoError(t, s.put(successionRecord{Old: "dddd", Phase: successionHandoff}))
	require.NoError(t, s.remove("dddd"))

	again := newSuccessions(path)
	require.Nil(t, again.of("aaaa"), "nothing is read before the first use")
	again.load()
	require.Equal(t, successionHandoff, again.of("aaaa").Phase)
	require.Equal(t, int64(7), again.of("aaaa").DeadlineMS)
	require.Equal(t, "bbbb", again.succeededBy("cccc").Old)
	require.Equal(t, []string{"@r"}, again.of("bbbb").Roles)
	require.True(t, again.of("bbbb").Purge, "a restart mid-ramp must still purge")
	require.Nil(t, again.of("dddd"))
	require.Nil(t, again.succeededBy("aaaa"))
}
