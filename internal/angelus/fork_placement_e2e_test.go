package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A successor's fork cut inside the first holder's turns used to hang under
// the first holder; it belongs under the aria that was forked.
func TestAForkCutInAnAncestorsTurnsSitsUnderTheAriaForked(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	first := threeTurnAria(t, a, acli, ctx)
	successor, err := acli.Fork(ctx, first, 3, 0, nil, nil)
	require.NoError(t, err)

	for turn, want := range map[uint64][]string{1: nil, 2: {"prompt 1"}} {
		child, err := acli.Fork(ctx, successor.Alternative, turn, 0, nil, nil)
		require.NoError(t, err)
		node, ok := a.Backend.Node(child.Alternative)
		require.True(t, ok)
		require.Equal(t, successor.Alternative, node.Parent, "fork at turn %d", turn)
		require.Equal(t, want, promptsOf(t, acli, ctx, child.Alternative), "fork at turn %d reads the same turns", turn)
	}
}
