package angelus_test

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

// The attendance keeps a back/forward history: every bind is an entry, a hop
// moves the cursor and rebinds, and the cursor is a position, so a figaro
// visited twice is two entries and forward goes on past the first.
func TestAHopWalksTheShellsHistoryAndRebinds(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	pid := os.Getpid()
	ids := map[string]string{}
	for _, n := range []string{"A", "B", "C"} {
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		ids[n] = c.FigaroID
	}
	for _, n := range []string{"A", "B", "A", "C"} {
		require.NoError(t, acli.Bind(ctx, pid, ids[n], 0))
	}
	r, err := acli.Resolve(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, []string{ids["A"], ids["B"], ids["A"], ids["C"]}, r.History)
	require.Equal(t, 3, r.Pos)

	hop := func(steps int, want string, pos int) {
		t.Helper()
		h, err := acli.Hop(ctx, pid, steps)
		require.NoError(t, err)
		require.True(t, h.Moved)
		require.Equal(t, ids[want], h.FigaroID)
		require.Equal(t, pos, h.Pos)
		bound, _, _ := a.Registry.Resolve(pid)
		require.Equal(t, ids[want], bound, "the hop rebinds the shell")
	}
	hop(-3, "A", 1)
	hop(1, "B", 2)
	hop(1, "A", 3)
	hop(1, "C", 4)
	h, err := acli.Hop(ctx, pid, 1)
	require.NoError(t, err)
	require.False(t, h.Moved)
	require.Equal(t, 4, h.Pos)
	require.Equal(t, 4, h.Total)

	hop(-2, "B", 2)
	c, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	require.NoError(t, acli.Bind(ctx, pid, c.FigaroID, 0))
	r, err = acli.Resolve(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, []string{ids["A"], ids["B"], c.FigaroID}, r.History, "an attend after a back drops what was ahead")
}

func TestAHopStepsOverAKilledFigaroAndSaysSo(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	pid := os.Getpid()
	var ids []string
	for range 3 {
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		ids = append(ids, c.FigaroID)
		require.NoError(t, acli.Bind(ctx, pid, c.FigaroID, 0))
	}
	_, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: ids[1]})
	require.NoError(t, err)
	h, err := acli.Hop(ctx, pid, -1)
	require.NoError(t, err)
	require.True(t, h.Moved)
	require.Equal(t, ids[0], h.FigaroID)
	require.Len(t, h.Dropped, 1)
	require.Equal(t, ids[1], h.Dropped[0].ID)
	require.Contains(t, []string{"gone", "reaped"}, h.Dropped[0].Why)
	require.Equal(t, 1, h.Pos)
	require.Equal(t, 2, h.Total)
}

// Hops on one shell are one writer's: three at once are three steps.
func TestConcurrentHopsAreThreeSteps(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	pid := os.Getpid()
	var ids []string
	for range 4 {
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		ids = append(ids, c.FigaroID)
		require.NoError(t, acli.Bind(ctx, pid, c.FigaroID, 0))
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			_, err := acli.Hop(ctx, pid, -1)
			require.NoError(t, err)
		})
	}
	wg.Wait()
	r, err := acli.Resolve(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, ids[0], r.FigaroID)
	require.Equal(t, 0, r.Pos)
	require.Len(t, r.History, 4)
}

func TestBackFromHomeReturnsToWhereTheShellLeft(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	pid := os.Getpid()
	var ids []string
	for range 2 {
		c, err := acli.Create(ctx, dress(t, "mock"), nil)
		require.NoError(t, err)
		ids = append(ids, c.FigaroID)
		require.NoError(t, acli.Bind(ctx, pid, c.FigaroID, 0))
	}
	require.NoError(t, acli.Unbind(ctx, pid))
	h, err := acli.Hop(ctx, pid, -1)
	require.NoError(t, err)
	require.Equal(t, ids[1], h.FigaroID)
	h, err = acli.Hop(ctx, pid, -1)
	require.NoError(t, err)
	require.Equal(t, ids[0], h.FigaroID)
}

// The history reads as a form, per shell, and nothing may write it.
func TestTheHistoryReadsAsAReadOnlyForm(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	pid := os.Getpid()
	c, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	require.NoError(t, acli.Bind(ctx, pid, c.FigaroID, 0))

	conn := dialAria(t, a, rpc.NavFormID(pid), nil)
	var out rpc.FormResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &out))
	raw, ok := out.Snapshot.Get("entries.01.id")
	require.True(t, ok, "no entries in %v", out.Snapshot)
	require.Contains(t, string(raw), c.FigaroID)
	here, ok := out.Snapshot.Get("entries.01.here")
	require.True(t, ok)
	require.Contains(t, string(here), "true")
	require.NotNil(t, out.View)
	require.Equal(t, rpc.FormReadOnly, out.Access.Mode)

	var set rpc.SetResponse
	require.Error(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{"pos": "9"}), &set))
}
