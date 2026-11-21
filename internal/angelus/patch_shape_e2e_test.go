package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

// El Zapador's gateway smoke: a set whose patch was a plain {key: value}
// dict was accepted and changed nothing.
func TestASetWhosePatchIsAPlainDictIsRefusedNamingTheShape(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": map[string]any{"mantra": "gateway check"}}))
	require.NotNil(t, reply.Error, "a plain dict was taken as a patch")
	require.Contains(t, reply.Error.Message, `"mantra" is not a patch field`)
	require.Contains(t, reply.Error.Message, `{"Object":{"Set":`)
	require.NotEqual(t, "gateway check", deref(delegatedFormOf(t, c, r.aria).Snapshot.Lookup("mantra")))
}
