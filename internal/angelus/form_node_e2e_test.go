package angelus_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

func rawPatch(kv map[string]string) *rpc.FormPatch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	p := form.Build(form.Snapshot{}, set, nil)
	return &p
}

// An unbound form is minted, read, patched and watched with no agent ever constructed.
func TestFormNodeLifecycleWithoutAnAgent(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"deploy tracker"`}))
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

	var form rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &form))
	raw, ok := form.Snapshot.Get("name")
	require.True(t, ok)
	require.Equal(t, `"deploy tracker"`, string(raw))

	// The delta fans out from the hub, which is the notifier when no agent is.
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
		_, hasK := delta.Patch.Entry("status.phase")
		require.True(t, hasK)
	case <-time.After(5 * time.Second):
		t.Fatal("no form.delta reached the attached client")
	}

	var after rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &after))
	raw, ok = after.Snapshot.Get("status.phase")
	require.True(t, ok)
	require.Equal(t, `"canary"`, string(raw))

	// A turn-shaped method on a form is refused by name, never a wake attempt.
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a form, not a figaro")
	require.Contains(t, err.Error(), "bind")

	require.Nil(t, a.Registry.Get(created.FormID), "a form acquired an agent")
}

// The naked-figaro remedy: a set that does not wake is the only way to patch provider keys in.
func TestDormantAriaSetDoesNotWake(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, nil, nil)
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

func TestFormCreateParentRules(t *testing.T) {
	_, acli, ctx := daemonFixture(t)

	parent, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"k": `1`}))
	require.NoError(t, err)
	child, err := acli.FormCreate(ctx, parent.FormID, nil, rawPatch(map[string]string{"who": `"child"`}))
	require.NoError(t, err)
	require.NotEqual(t, parent.FormID, child.FormID)
	require.True(t, strings.HasPrefix(child.FormID, "@"))

	aria, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	_, err = acli.FormCreate(ctx, aria.FigaroID, nil, rawPatch(map[string]string{"x": `1`}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an unbound form")

	_, err = acli.FormCreate(ctx, "", nil, &rpc.FormPatch{})
	require.Error(t, err, "an empty birth patch must be refused")
}

func TestListRecencyDoesNotWakeDormantArias(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.NoError(t, a.Registry.Kill(created.FigaroID))
	require.Nil(t, a.Registry.Get(created.FigaroID))

	list, err := acli.List(ctx)
	require.NoError(t, err)
	var row *rpc.FigaroInfoResponse
	for i := range list.Figaros {
		if list.Figaros[i].ID == created.FigaroID {
			row = &list.Figaros[i]
		}
	}
	require.NotNil(t, row)
	require.Equal(t, "dormant", row.State)
	require.NotZero(t, row.LastActive, "recency missing: figwal timestamps did not reach the row")
	require.Nil(t, a.Registry.Get(created.FigaroID), "listing recency woke the aria")
}

// bind births dormant: no agent, provider or registry entry until first use.
func TestFormBindBirthsDormantFigaro(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"team.goal": `"ship it"`}))
	require.NoError(t, err)

	bound, err := acli.FormBind(ctx, created.FormID, []string{"mock"}, nil)
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(bound.FigaroID, "@"), "bound figaro %q carries the form sigil", bound.FigaroID)
	require.Nil(t, a.Registry.Get(bound.FigaroID), "bind constructed an agent")

	conn, err := net.Dial("unix", bound.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), nil)
	defer client.Close()
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var form rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &form))
	raw, ok := form.Snapshot.Get("team.goal")
	require.True(t, ok, "form state did not inherit")
	require.Equal(t, `"ship it"`, string(raw))
	raw, ok = form.Snapshot.Get("aria_id")
	require.True(t, ok)
	require.Equal(t, `"`+bound.FigaroID+`"`, string(raw))
	require.Nil(t, a.Registry.Get(bound.FigaroID), "a read woke the bound figaro")

	// The parent form goes on living, unconverted.
	_, err = acli.FormCreate(ctx, created.FormID, nil, rawPatch(map[string]string{"who": `"sibling"`}))
	require.NoError(t, err, "the form stopped being forkable after binding")
}

func TestBindNullFailsAtFirstTurnNotAtMint(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	bound, err := acli.FormBind(ctx, "null", nil, nil)
	require.NoError(t, err, "bind null must mint")
	require.Nil(t, a.Registry.Get(bound.FigaroID))

	conn, err := net.Dial("unix", bound.Endpoint.Address)
	require.NoError(t, err)
	defer conn.Close()
	client := jkrpc.NewClient(jkrpc.NewConn(conn), nil)
	defer client.Close()
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.Error(t, err, "a provider-less figaro answered a turn")

	// The remedy goes through the same dormant path.
	var set rpc.SetResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodSet, rpc.SetRequest{
		Patch: *rawPatch(map[string]string{
			"system.provider": `"mock"`, "system.model": `"mock-model"`,
		}),
	}, &set))
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.NoError(t, err, "the patched naked figaro should take its first turn")
}

// Two creates share one parent form; reload with unchanged files is a no-op;
// a changed file or a hand-patched form remints on the next create.
func TestDefaultFormLifecycle(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)

	one, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	two, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)

	parentOf := func(id string) string {
		n, ok := a.Backend.Node(id)
		require.True(t, ok)
		return n.Parent
	}
	p1, p2 := parentOf(one.FigaroID), parentOf(two.FigaroID)
	require.Equal(t, p1, p2, "two creates did not share the default form")
	require.True(t, strings.HasPrefix(p1, "@"), "default parent %q is not a form", p1)

	// Unchanged files: the next create reuses.
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	three, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.Equal(t, p1, parentOf(three.FigaroID), "unchanged files reminted the default form")

	// Changed file: the next create remints.
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
mantra = "v2"
`), 0600))
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	four, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, p1, parentOf(four.FigaroID), "changed files did not remint")

	// A hand-patched default form remints: the patch must not reach every future aria.
	p4 := parentOf(four.FigaroID)
	_, err = a.Backend.ApplyForm(p4, *rawPatch(map[string]string{"sneaky": `true`}))
	require.NoError(t, err)
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	five, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, p4, parentOf(five.FigaroID), "a hand-patched default form was reused after reload")
}

// -O overrides the default; arias naming the same outfit share one parent,
// keyed by (name, content version).
func TestNamedOutfitParentIsShared(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)

	parentOf := func(id string) string {
		n, ok := a.Backend.Node(id)
		require.True(t, ok)
		return n.Parent
	}

	require.NoError(t, os.WriteFile(dir+"/outfits/other.toml", []byte(`
[system]
provider = "mock"
model = "other-model"
`), 0600))

	one, err := acli.Create(ctx, dress(t, "other"), nil)
	require.NoError(t, err)
	two, err := acli.Create(ctx, dress(t, "other"), nil)
	require.NoError(t, err)
	p1 := parentOf(one.FigaroID)
	require.Equal(t, p1, parentOf(two.FigaroID), "two creates on one outfit did not share a parent")

	// A different outfit is a different parent, and the default is a third.
	byDefault, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, p1, parentOf(byDefault.FigaroID),
		"a named outfit shared the default form's parent")

	// The named closure is what the aria wears, not the default plus it.
	snap, err := a.Backend.FormState(one.FigaroID)
	require.NoError(t, err)
	model, ok := snap.Get("system.model")
	require.True(t, ok)
	require.JSONEq(t, `"other-model"`, string(model), "-O did not override the default outfit")

	// Change the file: the next create lands on a different node.
	require.NoError(t, os.WriteFile(dir+"/outfits/other.toml", []byte(`
[system]
provider = "mock"
model = "other-model-v2"
`), 0600))
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	three, err := acli.Create(ctx, dress(t, "other"), nil)
	require.NoError(t, err)
	require.NotEqual(t, p1, parentOf(three.FigaroID), "a changed outfit file was not reminted")
}
