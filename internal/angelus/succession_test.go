package angelus

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
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

// The startup path publishes every catalog. It must not open successions.json:
// only a read of @successions, a turn end or the sweep does.
func TestPublishingCatalogsDoesNotReadSuccessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "successions.json")
	require.NoError(t, newSuccessions(path).put(successionRecord{Old: "aaaa", Phase: successionHandoff, DeadlineMS: 7}))

	a := &Angelus{Hubs: newHubs()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); a.Hubs.closeAll() })
	h := &handlers{angelus: a, ctx: ctx, succ: newSuccessions(path)}
	h.publishCatalogs()
	require.Nil(t, h.succ.of("aaaa"), "publishing the catalogs read successions.json")

	_, ok, err := h.readCatalog(rpc.SuccessionsFormID, rpc.MethodForm, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, h.succ.of("aaaa"), "a read of @successions did not load it")
}
