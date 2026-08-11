package angelus_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

func rawPatch(kv map[string]string) *rpc.FormPatch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return &rpc.FormPatch{Set: set}
}

// An unbound form lives a full life — minted, read, patched, watched —
// with NO agent ever constructed: the hub serves reads from the store and
// writes through the backend's single form writer, and fans the committed
// delta to attached clients itself.
func TestFormNodeLifecycleWithoutAnAgent(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.FormCreate(ctx, "", rawPatch(map[string]string{"name": `"deploy tracker"`}))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.FormID, "@"), "form id %q lacks the sigil", created.FormID)
	require.NotZero(t, created.Version)
	require.FileExists(t, created.Endpoint.Address, "endpoint not listening when FormCreate returned")

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	type note struct {
		method string
		params json.RawMessage
	}
	notes := make(chan note, 16)
	client := jkrpc.NewClient(jkrpc.NewConn(conn), func(m string, p json.RawMessage) {
		notes <- note{m, append(json.RawMessage(nil), p...)}
	})
	defer client.Close()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Read: served store-side.
	var form rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &form))
	raw, ok := form.Snapshot.Get("name")
	require.True(t, ok)
	require.Equal(t, `"deploy tracker"`, string(raw))

	// Write: served store-side, and the delta fans out to this attached
	// client because the hub is the notifier when no agent is.
	var set rpc.SetResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodSet, rpc.SetRequest{
		Patch: *rawPatch(map[string]string{"status.phase": `"canary"`}),
	}, &set))
	require.True(t, set.OK)
	require.Contains(t, set.Set, "status.phase")

	select {
	case n := <-notes:
		require.Equal(t, rpc.MethodFormDelta, n.method)
		var delta rpc.FormDelta
		require.NoError(t, json.Unmarshal(n.params, &delta))
		require.Equal(t, created.FormID, delta.AriaID)
		require.Greater(t, delta.Version, created.Version)
		require.Contains(t, delta.Patch.Set, "status.phase")
	case <-time.After(5 * time.Second):
		t.Fatal("no form.delta reached the attached client")
	}

	var after rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &after))
	raw, ok = after.Snapshot.Get("status.phase")
	require.True(t, ok)
	require.Equal(t, `"canary"`, string(raw))

	// A turn-shaped method gets a refusal that names the species and the
	// remedy — never a wake attempt, never a provider error.
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a form, not a figaro")
	require.Contains(t, err.Error(), "bind")

	// The whole life, agentless.
	require.Nil(t, a.Registry.Get(created.FormID), "a form acquired an agent")
}

// figaro.set on a DORMANT aria is served without waking it — the same
// write path forms use. This is also the naked-figaro remedy: a bind-null
// figaro whose wake fails for want of provider keys can only be repaired
// through a set that does not wake.
func TestDormantAriaSetDoesNotWake(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"))
	require.NoError(t, err)
	require.NoError(t, a.Registry.Kill(created.FigaroID))
	require.Nil(t, a.Registry.Get(created.FigaroID))

	conn, err := net.Dial("unix", created.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), nil)
	defer client.Close()
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var set rpc.SetResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodSet, rpc.SetRequest{
		Patch: *rawPatch(map[string]string{"mantra": `"asleep, and patched"`}),
	}, &set))
	require.True(t, set.OK)
	require.Nil(t, a.Registry.Get(created.FigaroID), "the set woke the aria")

	var form rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &form))
	raw, ok := form.Snapshot.Get("mantra")
	require.True(t, ok)
	require.Equal(t, `"asleep, and patched"`, string(raw))
	require.Nil(t, a.Registry.Get(created.FigaroID), "the read woke the aria")
}

// Form parentage over the wire: forms fork forms; conversations are
// refused by name.
func TestFormCreateParentRules(t *testing.T) {
	_, acli, ctx := daemonFixture(t)

	parent, err := acli.FormCreate(ctx, "", rawPatch(map[string]string{"k": `1`}))
	require.NoError(t, err)
	child, err := acli.FormCreate(ctx, parent.FormID, rawPatch(map[string]string{"who": `"child"`}))
	require.NoError(t, err)
	require.NotEqual(t, parent.FormID, child.FormID)
	require.True(t, strings.HasPrefix(child.FormID, "@"))

	aria, err := acli.Create(ctx, dress(t, "mock"))
	require.NoError(t, err)
	_, err = acli.FormCreate(ctx, aria.FigaroID, rawPatch(map[string]string{"x": `1`}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an unbound form")

	_, err = acli.FormCreate(ctx, "", &rpc.FormPatch{})
	require.Error(t, err, "an empty birth patch must be refused")
}
