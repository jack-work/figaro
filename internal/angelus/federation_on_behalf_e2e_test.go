package angelus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/sdk"
)

// A delegating service that reaches an aria on another node is forwarded
// as itself plus the user it names; the owner node applies its own
// [[authz.delegation]], so the send is recorded there under the user.
func TestADelegationIsForwardedToTheNodeHoldingTheAria(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)

	c, err := sdk.DialAria(sdk.Addr{Door: a.delegateDoor("gateway", "probe"), ID: id}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	_, err = c.Form(ctx)
	require.NoError(t, err)
	_, _, err = c.Qua(ctx, "through the gateway", nil)
	require.NoError(t, err)
	waitIdle(t, b.a, id)

	var page aria.Page
	require.NoError(t, dialAria(t, b.a, id, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))
	var senders []string
	for _, part := range page.Parts {
		for _, s := range part.InquirySegments {
			senders = append(senders, s.Sender)
		}
	}
	require.Equal(t, []string{"probe"}, senders)

	root, err := sdk.DialAngelus(a.delegateDoor("gateway", "probe"))
	require.NoError(t, err)
	t.Cleanup(func() { root.Close() })
	_, err = root.Kill(ctx, rpc.KillRequest{FigaroID: id})
	require.ErrorContains(t, err, "acts for user:probe@issuer.test.figaro")
}

// The owner node does not take the entry node's word for any of it: its own
// door refuses a delegation it does not list, a verb outside the ceiling,
// and a passage shaped so that an older node would admit the service alone.
func TestTheOwnerNodeRefusesWhatItDoesNotDelegate(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)
	d := &peer.Dialer{Members: a.members}
	token := a.issuer.Mint("gateway", time.Hour)
	open := func(p rpc.Passage) (*peer.Link, error) { return d.Open(ctx, b.id, p, nil) }

	l, err := open(rpc.Passage{Forwarded: true, OnBehalf: &rpc.OnBehalf{Bearer: token, User: "probe"}})
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	_, err = l.Call(ctx, rpc.MethodContext, mustJSON(t, map[string]string{rpc.AriaKey: id}))
	require.NoError(t, err)
	for _, m := range []string{rpc.MethodKill, rpc.MethodSet, rpc.MethodGC} {
		_, err = l.Call(ctx, m, mustJSON(t, map[string]string{rpc.AriaKey: id}))
		require.ErrorContains(t, err, "acts for user:probe@issuer.test.figaro", m)
	}

	_, err = open(rpc.Passage{Forwarded: true, OnBehalf: &rpc.OnBehalf{Bearer: token, User: "stranger"}})
	require.ErrorContains(t, err, "not listed to act for user:stranger@issuer.test.figaro")
	_, err = open(rpc.Passage{Forwarded: true, OnBehalf: &rpc.OnBehalf{Bearer: a.issuer.Mint("other-gateway", time.Hour), User: "probe"}})
	require.ErrorContains(t, err, "service:other-gateway@issuer.test.figaro is not listed")
	_, err = open(rpc.Passage{Forwarded: true, OnBehalf: &rpc.OnBehalf{Bearer: a.issuer.Mint("probe", time.Hour), User: "probe"}})
	require.ErrorContains(t, err, "is not listed to act for a user")
	_, err = open(rpc.Passage{Forwarded: true, Bearer: token, OnBehalf: &rpc.OnBehalf{Bearer: token, User: "probe"}})
	require.ErrorContains(t, err, "at once")
	_, err = open(rpc.Passage{Forwarded: true, OnBehalf: &rpc.OnBehalf{User: "probe"}})
	require.ErrorContains(t, err, "without its bearer")
	_, err = open(rpc.Passage{OnBehalf: &rpc.OnBehalf{Bearer: token, User: "probe"}})
	require.ErrorContains(t, err, "for no forwarded caller")
}
