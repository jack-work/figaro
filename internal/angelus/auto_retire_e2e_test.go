package angelus_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/sdk"
)

func ownerSets(t *testing.T, a *angelus.Angelus, id string, keys map[string]any) {
	t.Helper()
	var set rpc.SetResponse
	require.NoError(t, dialAria(t, a, id, nil).Call(context.Background(), rpc.MethodSet,
		rpc.SetRequest{Patch: *heldPatch(t, keys)}, &set))
}

func promptOn(ctx context.Context, a *angelus.Angelus, id, text string) error {
	c, err := sdk.DialAria(ariaAddr(a, id), nil)
	if err != nil {
		return err
	}
	defer c.Close()
	_, _, err = c.Qua(ctx, text, nil)
	return err
}

func retiredKey(t *testing.T, a *angelus.Angelus, id string) string {
	t.Helper()
	snap, err := a.Backend.FormState(id)
	require.NoError(t, err)
	if v := snap.Lookup("system.retired"); v != nil {
		return *v
	}
	return ""
}

// usageProvider reports 50,015 context tokens after turn 1 and 100,015 after
// turn 2, so a threshold of 75,000 sits between them.
func TestAnAriaRetiresAtTheEndOfTheTurnThatReachesItsThreshold(t *testing.T) {
	a, acli, ctx, _ := usageDaemon(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	ownerSets(t, a, id, map[string]any{"system.retirement": map[string]any{"context_threshold": 75_000, "ttl": "1h"}})

	turnOn(t, a, id, "below")
	row := listed(t, acli, ctx, id)
	require.Zero(t, row.RetiredAt, "retired below its threshold")
	require.Empty(t, retiredKey(t, a, id))

	turnOn(t, a, id, "crosses")
	row = listed(t, acli, ctx, id)
	require.NotZero(t, row.RetiredAt, "the turn that reached the threshold did not retire it")
	require.Equal(t, "context", row.RetiredReason)
	require.Equal(t, "context", retiredKey(t, a, id))

	err = promptOn(ctx, a, id, "one more")
	require.ErrorContains(t, err, "retired at its context threshold")

	meta, err := a.Backend.Meta(id)
	require.NoError(t, err)
	reaps := meta.CreatedAtMS + meta.TTLMS
	require.InDelta(t, meta.RetiredAtMS+time.Hour.Milliseconds(), reaps, float64(2*time.Second.Milliseconds()),
		"the ttl must count from retirement, not from birth")
}

func TestAPercentageThresholdIsOfTheContextWindow(t *testing.T) {
	a, acli, ctx, _ := usageDaemon(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	ownerSets(t, a, id, map[string]any{
		"system.max_context_tokens": 1_000_000,
		"system.retirement":         map[string]any{"context_threshold": "7.5%"},
	})

	turnOn(t, a, id, "5% of the window")
	require.Zero(t, listed(t, acli, ctx, id).RetiredAt, "50k of a 1M window is under 7.5%")
	turnOn(t, a, id, "10% of the window")
	row := listed(t, acli, ctx, id)
	require.NotZero(t, row.RetiredAt, "100k of a 1M window reaches 7.5%")
	require.Equal(t, "context", row.RetiredReason)
}

func TestAThresholdCrossedMidTurnLetsTheTurnAndTheQueueFinish(t *testing.T) {
	a, acli, ctx, p := usageDaemon(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	ownerSets(t, a, id, map[string]any{"system.retirement": map[string]any{"context_threshold": 75_000}})
	turnOn(t, a, id, "below")

	held, release := p.holdNext()
	require.NoError(t, promptOn(ctx, a, id, "in flight"))
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	require.NoError(t, promptOn(ctx, a, id, "queued behind it"), "a prompt queued before retirement must be taken")
	p.mu.Lock()
	p.hold = nil
	p.mu.Unlock()
	release()
	waitIdle(t, a, id)

	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.calls == 3
	}, 10*time.Second, 10*time.Millisecond, "the in-flight turn or the queued prompt was cut")
	waitIdle(t, a, id)
	require.Equal(t, "context", listed(t, acli, ctx, id).RetiredReason)
	require.ErrorContains(t, promptOn(ctx, a, id, "after"), "retired")
}

func TestAForkOfAnAriaRetiredByContextTakesPrompts(t *testing.T) {
	a, acli, ctx, _ := usageDaemon(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	ownerSets(t, a, id, map[string]any{"system.retirement": map[string]any{"context_threshold": 75_000}})
	turnOn(t, a, id, "one")
	turnOn(t, a, id, "two")
	require.Equal(t, "context", listed(t, acli, ctx, id).RetiredReason)

	for _, turn := range []uint64{0, 1} {
		fr, err := acli.Fork(ctx, id, turn, 0, nil, nil)
		require.NoError(t, err)
		child := fr.Alternative
		require.Zero(t, listed(t, acli, ctx, child).RetiredAt, "fork at turn %d inherited the retirement", turn)
		require.Empty(t, retiredKey(t, a, child))
		turnOn(t, a, child, "carry on")
	}
}
