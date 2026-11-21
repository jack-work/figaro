package angelus_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestADelegatedSetMayNotRewriteAFigarosAriaID(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	other, err := r.owner.Create(r.ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id, _ := json.Marshal(other.FigaroID)
	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": form.FromMap(map[string]json.RawMessage{"aria_id": id}).AsPatch()}))
	require.NotNil(t, reply.Error)
	require.Equal(t, r.aria, deref(delegatedFormOf(t, c, r.aria).Snapshot.Lookup("aria_id")))
}

func TestADelegatedSetMayNotGiveARuntimeRowChildren(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	for _, k := range []string{"truncation.x", "model.x", "token_budget.x"} {
		reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{k: "forged"})}))
		require.NotNil(t, reply.Error, k)
	}
}

func TestADelegatedSetMayNotMovePoolPlacement(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"worker.pool": "other"})}))
	require.NotNil(t, reply.Error)
}
