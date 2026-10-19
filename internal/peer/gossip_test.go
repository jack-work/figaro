package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/troupe"
)

func (lt *ledgerTroupe) admitByNode(by, node, door string) troupe.Entry {
	lt.t.Helper()
	inter, ik, err := lt.keep[by].Dir.Intermediate()
	require.NoError(lt.t, err)
	e, err := troupe.SignJoin(ik, inter, "estate", node, door, "user:owner", time.Now())
	require.NoError(lt.t, err)
	return e
}

func gossipFor(m *Ledger, v *Versions) *Gossip {
	return &Gossip{Members: m, Dialer: &Dialer{Members: m, Versions: v, Timeout: 2 * time.Second}, Quiet: time.Millisecond}
}

func knows(m *Ledger, node string) func() bool {
	return func() bool { _, ok := m.View().Members[node]; return ok }
}

// One exchange sends what this node holds and takes what the other holds,
// so both end with the same set.
func TestAnExchangeLeavesBothSidesEqual(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	base := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma := lt.membership("a", append(base, lt.admitByNode("a", "c", "c:1"))...)
	mb := lt.membership("b", append(base, lt.admitByNode("b", "d", "d:1"))...)
	lt.serveLedgerDoor("b", mb)
	require.NotEqual(t, ma.Version(), mb.Version())

	require.NoError(t, gossipFor(ma, &Versions{}).Exchange(context.Background(), "b"))
	require.Equal(t, ma.Version(), mb.Version())
	require.Contains(t, ma.Nodes(), "d")
	require.Contains(t, mb.Nodes(), "c")
}

// Any handshake with a peer whose digest differs pulls the difference, so
// a node that only ever forwards calls still converges.
func TestAHandshakeWithADriftedPeerPullsItsEntries(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	base := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma, mb := lt.membership("a", base...), lt.membership("b", base...)
	lt.serveLedgerDoor("b", mb)
	_, err := mb.Merge(lt.admitByNode("b", "d", "d:1"))
	require.NoError(t, err)

	v := &Versions{}
	g := gossipFor(ma, v)
	v.OnDrift(g.Drift(context.Background()))
	l, err := g.Dialer.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	l.Close()
	require.Eventually(t, knows(ma, "d"), 2*time.Second, 10*time.Millisecond)
}

// The periodic round reaches peers nobody has dialled.
func TestTheRoundRunsOnItsOwn(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	base := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma, mb := lt.membership("a", base...), lt.membership("b", base...)
	lt.serveLedgerDoor("b", mb)
	g := gossipFor(ma, &Versions{})
	g.Every = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go g.Run(ctx)

	_, err := mb.Merge(lt.admitByNode("b", "d", "d:1"))
	require.NoError(t, err)
	require.Eventually(t, knows(ma, "d"), 2*time.Second, 10*time.Millisecond)
}

// A node learning of a new member greets it at once rather than at the
// next round. The new member's door admits only nodes it already knows, as
// a joined node does: it took the signer's entries with its certificate.
func TestANewMemberIsGreeted(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	ma := lt.membership("a", lt.selfJoin("a"), lt.admitByNode("a", "c", "c:1"))
	mb := lt.membership("b", lt.selfJoin("a"), lt.selfJoin("b"))
	lt.serveLedgerDoor("b", mb)
	g := gossipFor(ma, &Versions{})
	ma.OnAdmit(g.Greet(context.Background()))

	_, err := ma.Merge(lt.selfJoin("b"))
	require.NoError(t, err)
	require.Eventually(t, knows(mb, "c"), 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return ma.Version() == mb.Version() }, 2*time.Second, 10*time.Millisecond)
}

// What a peer sends is checked like anything else: an entry signed outside
// the troupe is dropped and the rest is kept.
func TestAnExchangeKeepsWhatVerifiesAndDropsTheRest(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	other := newLedgerTroupe(t, "x")
	base := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma, mb := lt.membership("a", base...), lt.membership("b", base...)
	lt.serveLedgerDoor("b", mb)

	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	forged, err := troupe.SignRevoke(rk, "estate", "b", time.Now())
	require.NoError(t, err)
	l, err := gossipFor(ma, &Versions{}).Dialer.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	defer l.Close()
	_, err = l.TroupeEntries(context.Background(), rpc.TroupeEntriesRequest{Troupe: "estate", Entries: troupe.Encode([]troupe.Entry{forged, other.selfJoin("x"), lt.admitByNode("a", "c", "c:1")})})
	require.NoError(t, err)
	require.Contains(t, mb.Nodes(), "c")
	require.NotContains(t, mb.Nodes(), "x")
	require.Empty(t, mb.View().Gone)
}
