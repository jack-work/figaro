package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

func shadowedDoor(t *testing.T) onBehalfRig {
	t.Helper()
	r := onBehalfDoor(t)
	r.daemon.Policies.SetEnforcement(authz.EnforcementOf(config.AuthzConfig{}))
	require.False(t, r.daemon.Policies.Enforces(authz.KeyGrants), "an [authz] with no enforce_key_grants line shadows key grants")
	return r
}

func TestShadowedKeyGrantsStillRefuseADelegatedCreateNamingAScopedOutfit(t *testing.T) {
	r := shadowedDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	reply := c.call(rpc.MethodCreate, map[string]any{"outfits": []string{"scoped"}})
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "only the owner names an outfit that carries it")
}

func TestShadowedKeyGrantsStillRefuseADelegatedForkNamingAScopedOutfit(t *testing.T) {
	r := shadowedDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	reply := c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"outfits": []string{"scoped"}}))
	require.NotNil(t, reply.Error)
}

func TestShadowedKeyGrantsStillRefuseADelegatedSetOfOwnerHeldKeys(t *testing.T) {
	r := shadowedDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	for _, k := range []string{"system.ttl", "lifecycle.status", "target-aria", "name"} {
		reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{k: "1h"})}))
		require.NotNil(t, reply.Error, k)
	}
}

func TestShadowedKeyGrantsStillRefuseADelegatedForkWithASeat(t *testing.T) {
	r := shadowedDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	reply := c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"seat": "someone"}))
	require.NotNil(t, reply.Error)
}

func TestADelegatedCreateMayNotRemoveAnOwnerHeldKeyItsOutfitGives(t *testing.T) {
	r := onBehalfDoorWith(t, plainMockOutfit+"[lifecycle]\nstatus = \"routing-alias\"\n", "")
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	plain, err := r.owner.Create(r.ctx, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, delegatedFormOf(t, c, plain.FigaroID).Snapshot.Lookup(form.LifecycleStatusKey))
	strip := form.Patch{Object: &form.ObjectPatch{Delete: map[string]form.Value{"lifecycle": rv(`{"status":"routing-alias"}`)}}}
	before, err := r.owner.List(r.ctx)
	require.NoError(t, err)
	reply := c.call(rpc.MethodCreate, map[string]any{"patch": strip})
	require.NotNil(t, reply.Error)
	after, err := r.owner.List(r.ctx)
	require.NoError(t, err)
	require.Len(t, after.Figaros, len(before.Figaros))
}
