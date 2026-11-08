package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"

	"github.com/jack-work/jkauthz"
)

const ownersTrace = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func traceparentHandlers(t *testing.T) *handlers {
	t.Helper()
	peer, err := authz.NewPeercred(1000, config.PeercredConfig{})
	require.NoError(t, err)
	return &handlers{angelus: &Angelus{Peercred: peer}}
}

func quaParams(t *testing.T, key, tp string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"text": "hi", key: tp, "sender": "someone"})
	require.NoError(t, err)
	return b
}

func traceparentAfter(t *testing.T, h *handlers, ctx context.Context, params json.RawMessage) string {
	t.Helper()
	out, err := h.trustTraceparent(ctx, rpc.MethodQua, params)
	require.NoError(t, err)
	var req rpc.QuaRequest
	require.NoError(t, json.Unmarshal(out, &req))
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &raw))
	require.Contains(t, raw, "sender", "stripping must keep every other key")
	return req.TraceParent
}

func callerAs(p jkauthz.Principal) context.Context {
	return authz.WithSubject(context.Background(), jkauthz.Subject{Principal: p})
}

// A traceparent decides which trace a turn's spans land in. Only the owner's
// well-formed one is taken; anyone else's, or a malformed one, is dropped and
// the turn starts its own trace.
func TestOnlyTheOwnersWellFormedTraceparentReachesTheTurn(t *testing.T) {
	h := traceparentHandlers(t)
	owner := callerAs(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"})
	guest := callerAs(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "mallory"})
	box := callerAs(jkauthz.Principal{Kind: jkauthz.Kind(rpc.PrincipalSandbox), ID: "box0"})

	require.Equal(t, ownersTrace, traceparentAfter(t, h, owner, quaParams(t, "traceparent", ownersTrace)), "the owner's valid traceparent joins")
	require.Empty(t, traceparentAfter(t, h, guest, quaParams(t, "traceparent", ownersTrace)), "a guest's is dropped")
	require.Empty(t, traceparentAfter(t, h, box, quaParams(t, "traceparent", ownersTrace)), "a box's is dropped")
	require.Empty(t, traceparentAfter(t, h, context.Background(), quaParams(t, "traceparent", ownersTrace)), "an unknown caller's is dropped")
	require.Empty(t, traceparentAfter(t, h, guest, quaParams(t, "TraceParent", ownersTrace)), "a key in another case is dropped too")
	for _, bad := range []string{
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
		"00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-001",
		strings.Repeat("x", 55),
	} {
		require.Empty(t, traceparentAfter(t, h, owner, quaParams(t, "traceparent", bad)), "malformed %q from the owner is dropped", bad)
	}
}

func TestTraceparentGuardLeavesOtherMethodsAndPlainPromptsAlone(t *testing.T) {
	h := traceparentHandlers(t)
	guest := callerAs(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "mallory"})
	plain := json.RawMessage(`{"text":"hi","sender":"x"}`)
	out, err := h.trustTraceparent(guest, rpc.MethodQua, plain)
	require.NoError(t, err)
	require.Equal(t, string(plain), string(out))
	other := json.RawMessage(`{"traceparent":"anything"}`)
	out, err = h.trustTraceparent(guest, rpc.MethodSet, other)
	require.NoError(t, err)
	require.Equal(t, string(other), string(out))
}
