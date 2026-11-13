package angelus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func formOf(t *testing.T, n *fedNode, client, id string) (form.Snapshot, error) {
	t.Helper()
	c := n.ariaAs(client, id, nil)
	resp, err := c.Form(callCtx(t))
	if err != nil {
		return form.Snapshot{}, err
	}
	return resp.Snapshot, nil
}

func idsIn(snap form.Snapshot) []string {
	var out []string
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") {
			out = append(out, strings.Trim(string(v), `"`))
		}
	}
	return out
}

// @troupe names every member, this node with a pointer to its own @arias and
// each peer with a pointer to that peer's, qualified by the peer's node.
func TestTheTroupeIndexNamesEveryMember(t *testing.T) {
	a, b := newFedPair(t)
	snap, err := formOf(t, a, "probe", rpc.TroupeFormID)
	require.NoError(t, err)
	self, _ := snap.Get(a.id + ".form")
	peer, _ := snap.Get(b.id + ".form")
	require.JSONEq(t, `"@arias"`, string(self))
	require.JSONEq(t, `"`+b.id+`/@arias"`, string(peer))
	mine, _ := snap.Get(a.id + ".self")
	require.JSONEq(t, `true`, string(mine))
}

// Reading the index needs a role on the node, as list does.
func TestAStrangerCannotReadTheTroupeIndex(t *testing.T) {
	_, b := newFedPair(t)
	c, err := sdk.DialAria(sdk.Addr{Endpoint: b.callerEndpoint(b.issuer.Mint("intruder", time.Hour)), ID: rpc.TroupeFormID}, nil)
	if err == nil {
		defer c.Close()
		_, err = c.Form(callCtx(t))
	}
	require.Error(t, err, "a caller holding no role on b read b's troupe index")
}

// Two callers whose bearers ride through one node each get their own
// @arias from the peer: the peer keys the watch by the forwarded caller,
// never by the node that relayed it.
func TestTwoCallersThroughOneNodeEachGetTheirOwnPeerArias(t *testing.T) {
	a, b := newFedPair(t)
	owned := b.mint()
	peerArias := b.id + "/" + rpc.AriasFormID

	probe, err := formOf(t, a, "probe", peerArias)
	require.NoError(t, err)
	require.Contains(t, idsIn(probe), owned, "the owner-role caller does not see b's aria")

	stranger, err := formOf(t, a, "stranger", peerArias)
	require.NoError(t, err, "a caller holding a role on b cannot read its own @arias there")
	require.NotContains(t, idsIn(stranger), owned, "a caller relayed through a saw another caller's rows on b")
}

// A kill on a peer's aria is the peer's to judge: forwarded under the
// caller's own bearer, refused for a caller who may not kill it there.
func TestAForwardedKillIsJudgedByThePeer(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)
	_, err := a.as("stranger").Kill(ctx, rpc.KillRequest{FigaroID: b.id + "/" + id})
	require.Error(t, err, "b let a caller with only owner:self kill the owner's aria")
	_, err = a.as("probe").Kill(ctx, rpc.KillRequest{FigaroID: b.id + "/" + id})
	require.NoError(t, err)
}
