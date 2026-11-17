package angelus_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
)

// Defends #114: a replay delivers nothing in the original's name. When the
// replayed turn fails, the notice goes to the figaro that set the replay, never
// to whoever asked the original turn.
func TestAFailedReplayTellsWhoSetItNotWhoAsked(t *testing.T) {
	r := successionDaemon(t)
	asker := r.aria(nil)
	setter := r.aria(nil)
	old := r.aria(nil)
	agent, ok := r.a.Registry.Get(old).(*figaro.Agent)
	require.True(t, ok)
	done := make(doneSink, 1)
	unsub := agent.Subscribe(done)
	owner := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())})
	require.NoError(t, agent.SubmitPromptFrom(owner, rpc.QuaRequest{Text: "lane done, review it"}, asker))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorded turn never ended")
	}
	unsub()
	waitIdle(t, r.a, old)

	b := r.aria(nil)
	turnOn(t, r.a, b, "hello")
	r.p.set(func() { r.p.failFrom[b] = 2 })
	params, err := rpc.WithCaller(rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ReplayKey: map[string]any{"of": old, "turn": 1}})}, setter, nil)
	require.NoError(t, err)
	var set rpc.SetResponse
	require.NoError(t, dialAria(t, r.a, b, nil).Call(r.ctx, rpc.MethodSet, params, &set))

	r.eventually(func() bool { return containsFailure(r.heard(setter)) }, "the setter was not told the replay failed")
	waitIdle(t, r.a, b)
	waitIdle(t, r.a, asker)
	require.False(t, containsFailure(r.heard(asker)), "the original asker was told a replay failed")
}

func containsFailure(heard string) bool { return strings.Contains(heard, "failed (provider_error)") }
