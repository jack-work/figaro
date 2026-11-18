package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// An interior fork's sidecar holds the counts and context of a fold of its
// own history from LT 1, though the daemon now moves the parent's sidecar to
// the fork point instead of folding: at every turn, near the head and near
// the start, where each takes a different branch.
func TestAnInteriorForkHoldsWhatAFoldOfItsHistoryHolds(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	conn := dialAria(t, a, id, nil)
	const turns = 8
	for i := 0; i < turns; i++ {
		var out rpc.QuaResponse
		require.NoError(t, conn.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "a question long enough to estimate"}, &out))
		waitIdle(t, a, id)
	}
	for at := uint64(2); at <= turns; at++ {
		resp, err := acli.Fork(ctx, id, at, 0, nil, nil)
		require.NoError(t, err)
		meta, err := a.Backend.Meta(resp.Alternative)
		require.NoError(t, err)
		log, err := a.Backend.OpenFigIR(resp.Alternative)
		require.NoError(t, err)
		var want store.AriaMeta
		for _, e := range log.Read() {
			want.Fold(e)
		}
		require.Equal(t, want.MessageCount, meta.MessageCount, "messages at turn %d", at)
		require.Equal(t, want.TurnCount, meta.TurnCount, "turns at turn %d", at)
		require.Equal(t, want.ContextTokens, meta.ContextTokens, "context at turn %d", at)
		require.Equal(t, want.ContextExact, meta.ContextExact, "context exact at turn %d", at)
	}
}
