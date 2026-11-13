package angelus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/sdk"
)

// A root with a child and a grandchild, each woken so its agent is live.
func wokenSubtree(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) (string, []string) {
	t.Helper()
	root, child := withChild(t, acli, ctx)
	grand, err := acli.Fork(ctx, child, 0, 0, nil, nil)
	require.NoError(t, err)
	kids := []string{child, grand.Alternative}
	for _, id := range append([]string{root}, kids...) {
		var out rpc.QuaResponse
		require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "wake"}, &out), "waking %s", id)
		waitIdle(t, a, id)
		require.NotNil(t, a.Registry.Get(id), "control: %s has no live agent", id)
	}
	return root, kids
}

func listedByID(t *testing.T, acli *sdk.Angelus, ctx context.Context) map[string]rpc.FigaroInfoResponse {
	t.Helper()
	resp, err := acli.List(ctx)
	require.NoError(t, err)
	out := map[string]rpc.FigaroInfoResponse{}
	for _, f := range resp.Figaros {
		out[f.ID] = f
	}
	return out
}

// Edge, spain W29: after kill -r --now P, status and list still served C, a
// child the delete took, because only the root's agent and endpoint went.
func TestKillRNowLeavesNoTakenChildServed(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	resp, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root, Recursive: true, Now: true})
	require.NoError(t, err)
	require.Len(t, resp.Removed, 3)

	rows := listedByID(t, acli, ctx)
	for _, id := range append([]string{root}, kids...) {
		require.Nil(t, a.Registry.Get(id), "%s kept a live agent", id)
		_, still := rows[id]
		require.False(t, still, "list still serves %s", id)
		var out json.RawMessage
		require.Error(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodContext, nil, &out), "status still answers for %s", id)
		var sent rpc.QuaResponse
		require.Error(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "still there?"}, &sent), "a send to %s was taken", id)
		_, err := acli.Attach(ctx, id)
		require.Error(t, err, "an attach to %s was taken", id)
		require.Nil(t, a.Registry.Get(id), "a refused send or attach brought %s back", id)
	}
}

func TestKillRWithAGraceReapsEveryTakenChild(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	root, kids := wokenSubtree(t, a, acli, ctx)
	resp, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: root, Recursive: true, GraceMS: time.Hour.Milliseconds()})
	require.NoError(t, err)
	require.Len(t, resp.Reaped, 3)

	rows := listedByID(t, acli, ctx)
	for _, id := range append([]string{root}, kids...) {
		require.Nil(t, a.Registry.Get(id), "%s kept a live agent", id)
		row, ok := rows[id]
		require.True(t, ok && row.ReapedAt != 0, "list serves %s as live: %+v", id, row)
		var out rpc.QuaResponse
		err := dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hello?"}, &out)
		require.ErrorContains(t, err, "reaped", "%s still takes prompts", id)
	}
}
