package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

// A fork's listing row says what the fork holds and what it runs under. An
// interior fork at turn N shares the N-1 turns before it, so it counts their
// messages, not one turn more; and it runs under the provider, model and
// mantra its own form holds, which ls and status must show without waiting
// for it to run once.
func TestAForkListsWhatItHoldsAndRunsUnder(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	conn := dialAria(t, a, id, nil)
	set := func(mantra string) {
		var out rpc.SetResponse
		require.NoError(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{"mantra": mantra}), &out))
	}
	turn := func(text string) {
		var out rpc.QuaResponse
		require.NoError(t, conn.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: text}, &out))
		waitIdle(t, a, id)
	}
	set("before one")
	turn("one")
	set("before two")
	turn("two")

	fork := func(atTurn uint64) string {
		resp, err := acli.Fork(ctx, id, atTurn, 0, nil, nil)
		require.NoError(t, err)
		return resp.Alternative
	}
	cases := []struct {
		name string
		at   uint64
		msgs int
	}{
		{"A:1 shares nothing", 1, 0},
		{"A:2 shares turn 1", 2, 2},
		{"the head shares both", 0, 4},
	}
	forks := map[string]string{}
	for _, c := range cases {
		forks[c.name] = fork(c.at)
	}
	list, err := acli.List(ctx)
	require.NoError(t, err)
	rows := map[string]rpc.FigaroInfoResponse{}
	for _, f := range list.Figaros {
		rows[f.ID] = f
	}
	parent := rows[id]
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row, ok := rows[forks[c.name]]
			require.True(t, ok, "the fork is not listed")
			require.Equal(t, c.msgs, row.MessageCount, "messages")
			require.Equal(t, parent.Provider, row.Provider, "provider")
			require.Equal(t, parent.Model, row.Model, "model")
			var own rpc.FormResponse
			require.NoError(t, dialAria(t, a, forks[c.name], nil).Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &own))
			mantra := ""
			if v := own.Snapshot.Lookup("mantra"); v != nil {
				mantra = *v
			}
			require.Equal(t, mantra, row.Mantra, "mantra")
		})
	}
}
