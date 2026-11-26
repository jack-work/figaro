package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

func withTrunks(t *testing.T, b *store.XwalBackend, turn uint64) {
	t.Helper()
	if turn == 0 {
		require.NoError(t, wire.Install(b.Store(), t.TempDir(), wire.Capabilities{Trunks: true}))
	}
}

func capPredecessorAtZero(t *testing.T, s seated) {
	t.Helper()
	_, _, err := s.backend.Fork(s.pred)
	require.NoError(t, err)
	snap, err := s.backend.FormState(s.pred)
	require.NoError(t, err)
	none, _ := json.Marshal(0)
	_, err = s.backend.ApplyForm(s.pred, form.Build(snap, map[string]json.RawMessage{form.FanoutMaxChildrenKey: none}, nil))
	require.NoError(t, err)
}

func TestAKillingSuccessionRetiresItsPredecessorInTheForkForAMinute(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		ttl  time.Duration
		ok   bool
	}{
		"kill":         {[]string{"--ttl", "0"}, time.Minute, true},
		"ten minutes":  {nil, 10 * time.Minute, true},
		"an hour":      {[]string{"--ttl", "1h"}, time.Hour, true},
		"over an hour": {[]string{"--ttl", "2h"}, 0, false},
		"linger":       {[]string{"--abdication", "linger"}, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := succeedArgs(t, append(append([]string{"@r"}, tc.argv...), "--", "go")...)
			require.NoError(t, err)
			ttl, ok := forkRetireTTL(plan)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.ttl, ttl)
		})
	}
}

func TestASuccessionOverAnHourIsRefusedBeforeAnythingIsForked(t *testing.T) {
	s := seatedPredecessor(t, func(b *store.XwalBackend, _ string, turn uint64) { withTrunks(t, b, turn) })
	nodes := len(s.backend.Nodes())
	plan, err := succeedArgs(t, s.role, "--ttl", "2h", "--", "go")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "1h0m0s")
	require.False(t, r.Forked)
	require.Equal(t, nodes, len(s.backend.Nodes()))
}

func TestAKillingSuccessionOfAPredecessorAtItsCapIsAdmitted(t *testing.T) {
	s := seatedPredecessor(t, func(b *store.XwalBackend, _ string, turn uint64) { withTrunks(t, b, turn) })
	capPredecessorAtZero(t, s)
	plan, err := succeedArgs(t, s.role, "--ttl", "0", "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Equal(t, "prompt", r.FailedAt, "the fork was refused: %s", r.Error)
}

func TestARefusedSeatLeavesNoForkBehind(t *testing.T) {
	s := seatedPredecessor(t, func(b *store.XwalBackend, _ string, turn uint64) { withTrunks(t, b, turn) })
	capPredecessorAtZero(t, s)
	plan, err := succeedArgs(t, s.role, "--abdication", "linger", "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Equal(t, "place", r.FailedAt)
	require.False(t, r.Forked)
	require.Empty(t, r.Successor)
	require.Empty(t, r.recovery(plan))
	require.Len(t, s.backend.Reaped(), 1, "the refused succession left its fork alive")
}

func TestAPromptFailureLeavesTheSeatedSuccessorAndTheRetiredPredecessor(t *testing.T) {
	s := seatedPredecessor(t, func(b *store.XwalBackend, _ string, turn uint64) { withTrunks(t, b, turn) })
	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Equal(t, "prompt", r.FailedAt)
	require.True(t, r.Forked)
	require.True(t, r.retiredByFork)

	snap, err := s.backend.FormState(s.pred)
	require.NoError(t, err)
	require.True(t, store.RetiredIn(snap), "the predecessor was put back after the prompt failed")
	parent, _ := s.backend.HierarchyEngine().Drawn(s.pred)
	require.Equal(t, r.Successor, parent, "the successor lost its seat")
	require.Empty(t, s.backend.Reaped(), "the successor was discarded")

	steps := r.recovery(plan)
	require.Contains(t, steps, "figaro send -f --id "+r.Successor+" -- <the succession prompt>")
	var cast string
	for _, step := range steps {
		if len(step) > 12 && step[:12] == "figaro cast " {
			cast = step
		}
	}
	require.Contains(t, cast, "figaro cast "+r.Successor+" "+r.Role)
	require.Contains(t, cast, "reaped in 10m")
	for _, step := range steps {
		require.NotContains(t, step, "undo")
	}
}

func TestRecoveryWarnsThatOnlyTheOwnerCastsAfterTheReap(t *testing.T) {
	for name, tc := range map[string]struct {
		ttl  time.Duration
		ride time.Duration
		kill bool
	}{
		"ten minutes": {10 * time.Minute, 10 * time.Minute, false},
		"ttl zero":    {0, time.Minute, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := succeedReport{Role: "@r", Predecessor: "old", Successor: "new", Prompted: true, Forked: true,
				retiredByFork: true, retireTTL: tc.ride, FailedAt: "cast"}
			steps := strings.Join(r.recovery(succeedPlan{ttl: tc.ttl, abdication: abdicateRetire}), "\n")
			require.Contains(t, steps, "after the reap only the owner can cast")
			require.Equal(t, tc.kill, strings.Contains(steps, "with --ttl 0 the window is 1m"), steps)
		})
	}
}

func TestWithNoTrunksASuccessionFallsBackToTheOrdinaryPath(t *testing.T) {
	s := seatedPredecessor(t, nil)
	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.True(t, r.Forked, "the ordinary path made no fork: %v", err)
	require.False(t, r.retiredByFork)
	require.Contains(t, r.Warnings[0], "no trunks")
	snap, err := s.backend.FormState(s.pred)
	require.NoError(t, err)
	require.False(t, store.RetiredIn(snap), "a succession with no trunks retired its predecessor before the cast")
}
