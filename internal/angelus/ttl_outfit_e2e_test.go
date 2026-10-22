package angelus_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue #48: an outfit's [system] ttl reaches the board by layering, never by
// a patch that names it, and was never mirrored into the sweep's index.
func TestOutfitTTLIsIndexedAtCreateAndFork(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)
	require.NoError(t, os.WriteFile(dir+"/outfits/short.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
ttl = "1h"
`), 0600))

	created, err := acli.Create(ctx, dress(t, "short"), nil)
	require.NoError(t, err)
	meta, err := a.Backend.Meta(created.FigaroID)
	require.NoError(t, err)
	require.NotNil(t, meta, "no sidecar for an aria created from a ttl outfit")
	require.EqualValues(t, 3600000, meta.TTLMS, "the outfit's ttl did not reach the sidecar")

	fork, err := acli.Fork(ctx, created.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err)
	child, err := a.Backend.Meta(fork.Alternative)
	require.NoError(t, err)
	require.NotNil(t, child)
	require.EqualValues(t, 3600000, child.TTLMS, "a fork of a ttl outfit's aria lost the lifetime")
	require.Greater(t, child.CreatedAtMS, int64(0))
}

// A default outfit's ttl is for the arias it dresses: the shared default form
// they are forked from is a template, and reaping it would take every aria
// beneath it.
func TestDefaultOutfitTTLLeavesTheSharedFormUnleashed(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
ttl = "1h"
`), 0600))
	_, err := acli.OutfitReload(ctx)
	require.NoError(t, err)

	created, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	meta, err := a.Backend.Meta(created.FigaroID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.EqualValues(t, 3600000, meta.TTLMS, "the default outfit's ttl did not reach the aria")

	node, ok := a.Backend.Node(created.FigaroID)
	require.True(t, ok)
	shared, err := a.Backend.Meta(node.Parent)
	require.NoError(t, err)
	if shared != nil {
		require.Zero(t, shared.TTLMS, "the shared default form %s was given the outfit's lifetime", node.Parent)
	}
}
