package angelus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func setKeys(t *testing.T, set map[string]string, drop ...string) rpc.SetRequest {
	t.Helper()
	keys := map[string]json.RawMessage{}
	for k, v := range set {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	return rpc.SetRequest{Patch: form.Build(form.Snapshot{}, keys, drop)}
}

// A retired aria turns prompts away and still takes writes, so it can be
// dressed on the way out and brought back.
func TestARetiredAriaRefusesPromptsUntilUnretired(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	conn := dialAria(t, a, created.FigaroID, nil)

	var set rpc.SetResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodSet,
		setKeys(t, map[string]string{store.SystemRetiredKey: time.Now().UTC().Format(time.RFC3339), "system.ttl": "1h"}), &set))

	var out rpc.QuaResponse
	err = conn.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "are you there?"}, &out)
	require.Error(t, err, "a retired aria must refuse a prompt")
	require.Contains(t, err.Error(), "retired")

	require.NoError(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{"mantra": "epitaph"}), &set),
		"a retired aria must still take a write")

	require.NoError(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, nil, store.SystemRetiredKey), &set))
	require.NoError(t, conn.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "back again"}, &out),
		"an unretired aria must take prompts")
}

// A read is an access: it moves the stamp the sweep's keepalive counts from.
func TestAReadStampsTheAccessTheKeepaliveCounts(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	conn := dialAria(t, a, id, nil)
	var set rpc.SetResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{"system.ttl": "1h"}), &set))

	before := time.Now().UnixMilli()
	var page json.RawMessage
	require.NoError(t, dialAria(t, a, id, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))
	require.GreaterOrEqual(t, lastAccess(t, a, id), before, "the read left no access stamp")
}

// A fork carries its parent's sidecar across; retirement must not ride along,
// and the inherited lifetime must reach the sweep without a restart.
func TestAForkOfARetiredAriaTakesPrompts(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	conn := dialAria(t, a, created.FigaroID, nil)
	var set rpc.SetResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodSet,
		setKeys(t, map[string]string{store.SystemRetiredKey: "yes", "system.ttl": "30d"}), &set))

	fork, err := acli.Fork(ctx, created.FigaroID, 0, 0, nil, nil)
	require.NoError(t, err)
	child := fork.Alternative
	meta, err := a.Backend.Meta(child)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Zero(t, meta.RetiredAtMS, "the fork inherited its parent's retirement")
	require.Equal(t, (30 * 24 * time.Hour).Milliseconds(), meta.TTLMS, "the fork lost its inherited lifetime")

	var out rpc.QuaResponse
	require.NoError(t, dialAria(t, a, child, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "carry on"}, &out))

	lister, ok := a.Backend.(interface{ TTLEntries() []store.TTLEntry })
	require.True(t, ok)
	found := false
	for _, e := range lister.TTLEntries() {
		found = found || e.ID == child
	}
	require.True(t, found, "the fork's inherited lifetime is not in the sweep's index")
}

// ls marks a retired row whether or not its agent is resident.
func TestTheListingMarksARetiredAria(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	conn := dialAria(t, a, id, nil)
	var out rpc.QuaResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "wake"}, &out))
	var set rpc.SetResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{store.SystemRetiredKey: "yes"}), &set))

	retiredIn := func() int64 {
		resp, err := acli.List(ctx)
		require.NoError(t, err)
		for _, f := range resp.Figaros {
			if f.ID == id {
				return f.RetiredAt
			}
		}
		t.Fatalf("%s is not listed", id)
		return 0
	}
	require.NotNil(t, a.Registry.Get(id), "the aria should be resident for this half")
	require.NotZero(t, retiredIn(), "a resident retired aria lost its mark")
	require.NoError(t, a.Registry.Kill(id))
	require.NotZero(t, retiredIn(), "a dormant retired aria lost its mark")
}
