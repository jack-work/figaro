package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
)

type ownBytes interface {
	OwnBytes(string) int64
}

func bloat(t *testing.T, a *angelus.Angelus, id string, n int) {
	t.Helper()
	raw, _ := json.Marshal(strings.Repeat("x", n))
	_, err := backend(t, a).ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"ballast": raw}, nil))
	require.NoError(t, err)
}

func reapedState(t *testing.T, a *angelus.Angelus) map[string]any {
	t.Helper()
	var resp rpc.FormResponse
	require.NoError(t, dialAria(t, a, rpc.ReapedFormID, nil).Call(context.Background(), rpc.MethodForm, rpc.FormRequest{}, &resp))
	raw, err := json.Marshal(resp.Snapshot)
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(raw, &state))
	return state
}

func TestReapedCatalogCountsEachGroupsBytesAndTheirTotal(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	small, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	big, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	fork, err := acli.Fork(ctx, big.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err)
	bloat(t, a, big.FigaroID, 300<<10)
	bloat(t, a, fork.Alternative, 200<<10)
	age(t, a, small.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	age(t, a, big.FigaroID, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
	age(t, a, fork.Alternative, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
	a.SweepLifetimesNow()
	require.True(t, reaped(t, a, big.FigaroID))
	require.True(t, reaped(t, a, fork.Alternative))
	require.True(t, reaped(t, a, small.FigaroID))

	own := a.Backend.(ownBytes)
	state := reapedState(t, a)
	items := state["items"].(map[string]any)
	bytesOf := func(root string) int64 {
		it, ok := items[root].(map[string]any)
		require.True(t, ok, "%s is not in @reaped", root)
		return int64(it["bytes"].(float64))
	}
	wantBig := own.OwnBytes(big.FigaroID) + own.OwnBytes(fork.Alternative)
	require.Greater(t, wantBig, int64(500<<10), "the ballast is not on disk")
	require.Equal(t, wantBig, bytesOf(big.FigaroID), "a group counts every node it would purge")
	require.Equal(t, own.OwnBytes(small.FigaroID), bytesOf(small.FigaroID))
	require.Less(t, bytesOf(small.FigaroID), int64(100<<10))
	require.Equal(t, float64(wantBig+bytesOf(small.FigaroID)), state["bytes"])
}
