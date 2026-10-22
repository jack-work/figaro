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
