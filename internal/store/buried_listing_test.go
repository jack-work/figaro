package store

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// D1 (7): a node buried but not unlinked (a crash between the two) is never
// listed, and stays unlisted after the store is opened again.
func TestABuriedNodeIsNeverListed(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	require.NoError(t, err)
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	kept, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	zombie, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	require.NoError(t, be.SetMeta(zombie, &AriaMeta{ReapedAtMS: 1, ReapedRoot: zombie}))

	be.bury([]string{zombie})

	listed := func(be *XwalBackend) {
		t.Helper()
		require.True(t, be.Buried(zombie))
		require.Contains(t, be.ConversationIDs(), kept)
		require.NotContains(t, be.ConversationIDs(), zombie)
		for _, nodes := range [][]NodeView{be.Conversations(), be.Nodes()} {
			require.False(t, slices.ContainsFunc(nodes, func(n NodeView) bool { return n.ID == zombie }), "a buried node is listed")
		}
		for _, e := range be.Reaped() {
			require.NotEqual(t, zombie, e.ID, "a buried node is listed as reaped")
		}
	}
	listed(be)
	require.NoError(t, be.Close())

	again, err := NewXwalBackend(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { again.Close() })
	listed(again)
}
