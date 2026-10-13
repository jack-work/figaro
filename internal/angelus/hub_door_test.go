package angelus

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
)

type paramsAgent struct{ params chan json.RawMessage }

func (p *paramsAgent) Handle(_ context.Context, _ string, params json.RawMessage) (any, error) {
	p.params <- append(json.RawMessage(nil), params...)
	return map[string]any{}, nil
}

func (p *paramsAgent) Subscribe(figaro.Notifier) func() { return func() {} }

func policyHub(t *testing.T, policy authz.Policy) *ariaHub {
	t.Helper()
	hb := newAriaHub("abcd1234", t.TempDir()+"/aria.sock", policy)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); hb.Close() })
	require.NoError(t, hb.listen(ctx))
	return hb
}

// A forged x-internal-figaro-id reaches the policy as the bare asserted id,
// beside the connection's principal the params cannot touch.
func TestHubAttributesAForgedCallerAsAsserted(t *testing.T) {
	seen := make(chan authz.Identity, 1)
	hb := policyHub(t, authz.PolicyFunc(func(r authz.Request) authz.Decision {
		seen <- r.Identity
		return authz.Allow()
	}))
	agent := &paramsAgent{params: make(chan json.RawMessage, 1)}
	hb.bind(agent)

	client, _ := dialHub(t, hb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	params, err := rpc.WithCaller(rpc.QuaRequest{Text: "hi"}, "76062b18", nil)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, client.Call(ctx, rpc.MethodQua, params, &out))

	got := <-agent.params
	require.Equal(t, "76062b18", rpc.SenderFrom(got, nil))
	require.False(t, strings.HasPrefix(rpc.SenderFrom(got, nil), rpc.AriaLabelPrefix))

	id := <-seen
	require.Equal(t, "76062b18", id.FigaroID)
	require.NotEqual(t, rpc.PrincipalAria, id.Principal.Kind)
	if runtime.GOOS == "linux" {
		require.Equal(t, rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())}, id.Principal)
	}
}

func TestHubMethodsAreGuarded(t *testing.T) {
	hb := policyHub(t, authz.PolicyFunc(func(authz.Request) authz.Decision { return authz.Deny("door closed") }))
	hb.read = func(string, string, json.RawMessage) (any, bool, error) {
		t.Error("a denied request reached the store")
		return nil, true, nil
	}
	client, _ := dialHub(t, hb)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out map[string]any
	err := client.Call(ctx, rpc.MethodContext, struct{}{}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "door closed")
}
