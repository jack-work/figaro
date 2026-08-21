package angelus_test

import (
	"context"
	"encoding/json"
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
	return &rpc.FormPatch{Set: set}
}

// An unbound form lives a full life: minted, read, patched, watched -
// with NO agent ever constructed: the hub serves reads from the store and
// writes through the backend's single form writer, and fans the committed
// delta to attached clients itself.
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
	// remedy: never a wake attempt, never a provider error.
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a form, not a figaro")
	require.Contains(t, err.Error(), "bind")

	// The whole life, agentless.
	require.Nil(t, a.Registry.Get(created.FormID), "a form acquired an agent")
}

// figaro.set on a DORMANT aria is served without waking it: the same
// write path forms use. This is also the naked-figaro remedy: a bind-null
// figaro whose wake fails for want of provider keys can only be repaired
// through a set that does not wake.
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

// Form parentage over the wire: forms fork forms; conversations are
// refused by name.
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

// Listing recency now comes from figwal record timestamps, and reading
// it NEVER wakes a dormant aria. The row carries a real LastActive while
// the registry stays empty.
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

// form.bind births a DORMANT figaro from a form: state inherited,
// aria_id stamped, endpoint dialable, and no agent, no provider, no
// registry entry until first use. Dressed with a real outfit it then
// wakes and answers; naked (bind null) it mints fine and fails its
// first turn with the provider error, at the right time.
func TestFormBindBirthsDormantFigaro(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"team.goal": `"ship it"`}))
	require.NoError(t, err)

	bound, err := acli.FormBind(ctx, created.FormID, []string{"mock"}, nil)
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(bound.FigaroID, "@"), "bound figaro %q carries the form sigil", bound.FigaroID)
	require.Nil(t, a.Registry.Get(bound.FigaroID), "bind constructed an agent")

	// Inherited state + stamped identity, readable dormant.
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

// bind null mints the naked figaro; its first turn fails for want of a
// provider, at the turn, not at the mint.
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

	// The remedy: patch the provider in THROUGH THE SAME dormant path,
	// then the next turn wakes for real.
	var set rpc.SetResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodSet, rpc.SetRequest{
		Patch: *rawPatch(map[string]string{
			"system.provider": `"mock"`, "system.model": `"mock-model"`,
		}),
	}, &set))
	err = client.Call(callCtx, rpc.MethodQua, rpc.QuaRequest{Text: "hello"}, &struct{}{})
	require.NoError(t, err, "the patched naked figaro should take its first turn")
}

// The default-form lifecycle: fig new is bind-the-default-form. Two
// creates share ONE parent form (the reuse that shares the rendered
// prefix and the provider's cache); reload with unchanged files is a
// no-op; a changed outfit file remints on the NEXT create, not at
// reload; a hand-patched default form remints too: the ad-hoc patch is
// never silently propagated.
//
// Creates here name NO outfit, which is what `fig new` does. Naming one
// explicitly takes the other path now (-O overrides the default rather than
// layering on it) and is covered by TestNamedOutfitParentIsShared. The two
// used to be the same path, which is why this test used to say `-O mock`
// while describing the default.
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

	// Reload with UNCHANGED files: the next create reuses (no-op compute).
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	three, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.Equal(t, p1, parentOf(three.FigaroID), "unchanged files reminted the default form")

	// Change the outfit file: reload, and the NEXT create remints.
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

	// Hand-patch the (new) default form, reload: remint again: the patch
	// must not silently reach every future aria.
	p4 := parentOf(four.FigaroID)
	_, err = a.Backend.ApplyForm(p4, *rawPatch(map[string]string{"sneaky": `true`}))
	require.NoError(t, err)
	_, err = acli.OutfitReload(ctx)
	require.NoError(t, err)
	five, err := acli.Create(ctx, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, p4, parentOf(five.FigaroID), "a hand-patched default form was reused after reload")
}

// -O OVERRIDES the default, and arias naming the same outfit still share one
// parent: the sharing that keeps one rendered prefix and one warm provider
// cache per outfit, which is the whole reason a birth has a parent at all.
//
// The remint property comes from content addressing rather than from the
// default form's dirty flag: an outfit node is reused by (name, content
// version), so a changed file is a different node by construction.
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

	// A different outfit is a different parent, and the DEFAULT is a third:
	// naming an outfit must not land you on the default form.
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
