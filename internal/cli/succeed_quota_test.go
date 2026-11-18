package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func (s seated) labeled(t *testing.T, figaro string) seated {
	t.Helper()
	t.Setenv("FIGARO_ARIA", figaro)
	c, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	s.acli = c
	return s
}

func (s seated) setKeys(t *testing.T, id string, kv map[string]any) {
	t.Helper()
	snap, err := s.backend.FormState(id)
	require.NoError(t, err)
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		set[k] = raw
	}
	_, err = s.backend.ApplyForm(id, form.Build(snap, set, nil))
	require.NoError(t, err)
}

func (s seated) fan(t *testing.T, id string) (used int, quota *int) {
	t.Helper()
	resp, err := s.acli.ListWith(s.ctx, rpc.ListRequest{})
	require.NoError(t, err)
	for _, f := range resp.Figaros {
		if f.ID == id {
			return f.FanUsed, f.FanQuota
		}
	}
	t.Fatalf("%s has no row", id)
	return 0, nil
}

func fullPredecessor(t *testing.T) seated {
	t.Helper()
	trunks := t.TempDir()
	s := seatedPredecessor(t, func(b *store.XwalBackend, _ string, turn uint64) {
		if turn == 0 {
			require.NoError(t, wire.Install(b.Store(), trunks, wire.Capabilities{Trunks: true}))
		}
	})
	for range 2 {
		_, _, err := s.backend.Fork(s.pred)
		require.NoError(t, err)
	}
	s.setKeys(t, s.pred, map[string]any{form.FanoutQuotaKey: 2})
	used, quota := s.fan(t, s.pred)
	require.Equal(t, 2, used)
	require.NotNil(t, quota)
	return s.labeled(t, s.pred)
}

// Hard succession is net zero: the successor takes the predecessor's place
// and quota, so a predecessor with no budget left still hands over.
func TestAHardSucceedFitsAFullBudget(t *testing.T) {
	s := fullPredecessor(t)
	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	r := s.succeed(t, plan)
	require.Equal(t, r.Successor, r.DemotedUnder, "failed at %s: %s", r.FailedAt, r.Error)
	s.setKeys(t, s.pred, map[string]any{store.SystemRetiredKey: time.Now().UTC().Format(time.RFC3339)})
	used, quota := s.fan(t, r.Successor)
	require.NotNil(t, quota, "the successor did not take the quota")
	require.Equal(t, 2, *quota)
	require.Equal(t, 2, used, "the seat changed what the subtree uses")
	_, predQuota := s.fan(t, s.pred)
	require.Nil(t, predQuota, "the predecessor kept a quota under its successor")
}

// Familial succession keeps the predecessor alive under its successor, so it
// costs one, and a predecessor with no budget left is refused at the fork.
func TestALingeringSucceedCostsOneAndIsRefusedWithoutIt(t *testing.T) {
	s := fullPredecessor(t)
	plan, err := succeedArgs(t, s.role, "--abdication", "linger", "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Equal(t, "fork", r.FailedAt)
	require.Contains(t, r.Error, "free of quota 2")

	s.setKeys(t, s.pred, map[string]any{form.FanoutQuotaKey: 3})
	r = s.succeed(t, plan)
	require.Equal(t, r.Successor, r.DemotedUnder)
	used, quota := s.fan(t, r.Successor)
	require.Equal(t, 3, *quota)
	require.Equal(t, 3, used, "the lingering predecessor and its two children cost the successor 3")
}

// Overlap succession pays for its successor at the fork that makes it, and
// the seat then moves nothing in the budget.
func TestAnOverlapSucceedPaysAtItsOwnFork(t *testing.T) {
	s := fullPredecessor(t)
	_, err := s.acli.ForkWith(s.ctx, rpc.ForkRequest{FigaroID: s.pred, Patch: quotaZero()})
	require.Error(t, err, "an overlap successor was forked with no budget")
	s.setKeys(t, s.pred, map[string]any{form.FanoutQuotaKey: 3})
	out, err := s.acli.ForkWith(s.ctx, rpc.ForkRequest{FigaroID: s.pred, Patch: quotaZero()})
	require.NoError(t, err)
	plan, err := succeedArgs(t, s.role, "--successor", out.Alternative, "--", "you hold the seat")
	require.NoError(t, err)
	r, _ := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Equal(t, out.Alternative, r.DemotedUnder, "failed at %s: %s", r.FailedAt, r.Error)
	used, quota := s.fan(t, out.Alternative)
	require.Equal(t, 3, *quota)
	require.Equal(t, 3, used)
}

func quotaZero() *form.Patch {
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.FanoutQuotaKey: json.RawMessage(`0`)}, nil)
	return &p
}

// Gluck's invariant: a fork at an old turn copies no quota from the form as
// it stood then, and is charged by today's live tree.
func TestAForkAtAnOldTurnCopiesNoQuotaAndPaysTodaysTree(t *testing.T) {
	s := seatedPredecessor(t, func(b *store.XwalBackend, pred string, turn uint64) {
		if turn != 1 {
			return
		}
		snap, err := b.FormState(pred)
		require.NoError(t, err)
		_, err = b.ApplyForm(pred, form.Build(snap, map[string]json.RawMessage{form.FanoutQuotaKey: json.RawMessage(`5`)}, nil))
		require.NoError(t, err)
	})
	out, err := s.acli.ForkWith(s.ctx, rpc.ForkRequest{FigaroID: s.pred, AtTurn: 2})
	require.NoError(t, err)
	snap, err := s.backend.FormState(out.Alternative)
	require.NoError(t, err)
	_, copied := snap.Get(form.FanoutQuotaKey)
	require.False(t, copied, "a fork at turn 2 copied the quota the form held then")

	_, _, err = s.backend.Fork(s.pred)
	require.NoError(t, err)
	s.setKeys(t, s.pred, map[string]any{form.FanoutQuotaKey: 2})
	_, err = s.labeled(t, s.pred).acli.ForkWith(s.ctx, rpc.ForkRequest{FigaroID: s.pred, AtTurn: 1})
	require.Error(t, err, "a fork at turn 1 was charged by the tree as it stood then")
	require.Contains(t, err.Error(), "0 free of quota 2")
}
