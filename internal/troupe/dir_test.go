package troupe

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The bundle round-trips through disk, the directory and every file in it
// are the owner's alone, and node.crt carries the chain a peer needs.
func TestTheBundleRoundTripsAndIsPrivate(t *testing.T) {
	now := time.Now()
	rk, err := ParseRoot(rootPEM(t))
	require.NoError(t, err)
	root, err := RootCert("estate", rk)
	require.NoError(t, err)
	ik := newKey(t)
	inter, err := IssueIntermediate(rk, root, "estate", "n", ik.Public(), now, IntermediateTTL)
	require.NoError(t, err)
	nk := newKey(t)
	leaf, err := IssueNode(ik, inter, "estate", "n", nk.Public(), now, NodeTTL)
	require.NoError(t, err)

	d := DirFor(t.TempDir(), "estate")
	require.NoError(t, d.WriteRoot(root))
	require.NoError(t, d.WriteIntermediate(inter, ik))
	require.NoError(t, d.WriteNode(leaf, inter, nk))

	gotRoot, err := d.Root()
	require.NoError(t, err)
	require.Equal(t, root.Raw, gotRoot.Raw)
	gotInter, gotKey, err := d.Intermediate()
	require.NoError(t, err)
	require.Equal(t, inter.Raw, gotInter.Raw)
	require.Equal(t, ik, gotKey)
	pair, err := d.Node()
	require.NoError(t, err)
	require.Len(t, pair.Certificate, 2)
	require.Equal(t, leaf.Raw, pair.Leaf.Raw)

	st, err := os.Stat(string(d))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	entries, err := os.ReadDir(string(d))
	require.NoError(t, err)
	for _, e := range entries {
		info, err := e.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), e.Name())
	}
}
