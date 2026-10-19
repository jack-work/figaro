package angelus_test

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/sdk"
)

const ciKey = "service:ci@troupe:fed"

func keyPair(t *testing.T) (*fedNode, *fedNode) {
	t.Helper()
	pki := peertest.NewPKI(t, "fed")
	is := peertest.NewIssuer(t)
	a, b := openFedNode(t, is, ciKey), openFedNode(t, is, ciKey)
	a.serve(pki, b)
	b.serve(pki, a)
	a.start()
	b.start()
	return a, b
}

func grantBoth(t *testing.T, k apikey.Key, nodes []string, ns ...*fedNode) {
	t.Helper()
	g := authz.APIKeyGrant{ID: k.ID, Principal: ciKey, Hash: k.Hash[:], NotAfter: time.Now().Add(time.Hour), Nodes: nodes}
	for _, n := range ns {
		require.NoError(t, n.keys.SetTroupe([]authz.APIKeyGrant{g}))
	}
}

// A key good on node A alone reaches A's arias, and a call it makes for an
// aria on node B is refused at A before anything is sent: B's door never
// sees the key. Sent to B's door anyway, it is refused there. Named for
// both nodes, the same call goes through.
func TestAKeyIsNeverForwardedOutsideItsNodes(t *testing.T) {
	a, b := keyPair(t)
	onB, onA := b.mint(), a.mint()
	ctx := callCtx(t)
	wire, k, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	grantBoth(t, k, []string{a.id}, a, b)

	local, err := sdk.DialAria(sdk.Addr{Door: a.keyDoor(wire), ID: onA}, nil)
	require.NoError(t, err)
	defer local.Close()
	_, err = local.Form(ctx)
	require.NoError(t, err, "the key works on its own node")

	far, err := sdk.DialAria(sdk.Addr{Door: a.keyDoor(wire), ID: onB}, nil)
	require.NoError(t, err)
	defer far.Close()
	_, err = far.Form(ctx)
	require.ErrorContains(t, err, "not sent there")
	require.Zero(t, b.keysAt.Load(), "the key reached the node it does not name")

	d := &peer.Dialer{Members: a.members}
	_, err = d.Open(ctx, b.id, rpc.Passage{Forwarded: true, Bearer: wire}, nil)
	require.ErrorContains(t, err, "not good on node "+b.id)
	require.EqualValues(t, 1, b.keysAt.Load())

	nodes := []string{a.id, b.id}
	if nodes[0] > nodes[1] {
		nodes[0], nodes[1] = nodes[1], nodes[0]
	}
	grantBoth(t, k, nodes, a, b)
	both, err := sdk.DialAria(sdk.Addr{Door: a.keyDoor(wire), ID: onB}, nil)
	require.NoError(t, err)
	defer both.Close()
	_, err = both.Form(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, b.keysAt.Load())
}
