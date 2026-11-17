package angelus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func replayOn(t *testing.T, r *successionRig, id, of string) {
	t.Helper()
	ownerSets(t, r.a, id, map[string]any{form.ReplayKey: map[string]any{"of": of, "turn": 1}})
	r.eventually(func() bool {
		marks := replayMarks(t, r.acli, id)
		return len(marks) > 0 && marks[len(marks)-1] != nil
	}, "the replay never ran")
	waitIdle(t, r.a, id)
	r.hs.WaitSuccessionsForTest()
}

// Defends #114: a replay is a re-run, not more life. A replayed turn past the
// threshold mints no successor, hands over no role and retires nothing, and is
// not warned: the warning would ride the replayed inquiry.
func TestAReplayIsNeitherWarnedNorSucceeded(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(nil)
	turnOn(t, r.a, old, "the recorded work")
	b := r.aria(succeeding(nil))
	role := r.role(b, map[string]any{"duty": "answer"})
	r.p.set(func() { r.p.usage[b] = 61_000 })
	turnOn(t, r.a, b, "reaches eighty")
	before := len(r.arias())

	replayOn(t, r, b, old)
	require.NotContains(t, r.heard(b), "Start handing over", "the warning rode the replayed inquiry")
	require.False(t, r.has(b, "system.succession_warned"))

	r.p.set(func() { r.p.usage[b] = 100_000 })
	c, err := sdk.DialAria(ariaAddr(r.a, b), nil)
	require.NoError(t, err)
	defer c.Close()
	unsetReplay(t, c, boardOf(t, r.acli, r.ctx, b))
	ownerSets(t, r.a, b, map[string]any{form.ReplayKey: map[string]any{"of": old, "turn": 1}})
	r.eventually(func() bool { return len(replayMarks(t, r.acli, b)) >= 3 }, "the second replay never ran")
	waitIdle(t, r.a, b)
	r.hs.WaitSuccessionsForTest()
	time.Sleep(200 * time.Millisecond)
	require.Len(t, r.arias(), before, "a replay minted a successor")
	require.Empty(t, r.hs.SuccessionPhase(b))
	require.False(t, r.retired(b))
	require.Equal(t, b, r.holder(role), "a replay handed its role away")
	require.NotContains(t, r.heard(b), "retirement threshold")
}

func TestAReplayDoesNotRetireAtTheThreshold(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(nil)
	turnOn(t, r.a, old, "the recorded work")
	b := r.aria(map[string]any{"context_threshold": succeedAt, "ttl": "1h", "action": store.RetirementRetire})
	turnOn(t, r.a, b, "hello")
	r.p.set(func() { r.p.usage[b] = 100_000 })
	replayOn(t, r, b, old)
	require.False(t, r.retired(b), "a replay retired at the threshold")
	require.False(t, store.RetiredIn(boardOf(t, r.acli, r.ctx, b)))
}

func TestReplayWaitsForASuccessionToEnd(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(nil)
	turnOn(t, r.a, old, "the recorded work")
	b := r.aria(succeeding(nil))
	r.p.set(func() { r.p.holdFrom[b] = 2; r.p.usage[b] = 100_000 })
	require.NoError(t, promptOn(r.ctx, r.a, b, "the work that crosses"))
	r.eventually(func() bool { return r.hs.SuccessionPhase(b) != "" }, "no succession began")
	var set rpc.SetResponse
	err := dialAria(t, r.a, b, nil).Call(r.ctx, rpc.MethodSet,
		rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ReplayKey: map[string]any{"of": old, "turn": 1}})}, &set)
	require.ErrorContains(t, err, "succession")
	_, replaying := form.ReadReplay(boardOf(t, r.acli, r.ctx, b))
	require.False(t, replaying)
	require.NotContains(t, r.heard(b), "the recorded work")
}
