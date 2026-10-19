package troupe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	bodyA = "[roles.r]\ngrants = [{ verbs = [\"read\"], on = [\"*\"] }]\n"
	bodyB = "[roles.r]\ngrants = [{ verbs = [\"send\"], on = [\"*\"] }]\n"
)

func (s *estate) policy(by string, version uint64, body string, at time.Duration) Entry {
	s.t.Helper()
	e, err := SignPolicy(s.ikey[by], s.inter[by], "estate", version, "user:jack@auth.kelliher.info", body, s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

func TestTheHighestPolicyIsInForce(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.policy("aaaa0001", 1, bodyA, time.Second), s.policy("bbbb0002", 2, bodyB, 2*time.Second))
	v := l.View()
	require.NotNil(t, v.Policy)
	require.Equal(t, uint64(2), v.Policy.Version)
	require.Equal(t, "bbbb0002", v.Policy.Node)
	require.Empty(t, v.Rivals)
}

// Two admins writing the same version on two nodes converge everywhere on
// one of them, and the other is kept visible rather than silently lost.
func TestTwoWritesAtOneVersionConvergeAndTheLoserIsNamed(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	joins := []Entry{s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0)}
	pa, pb := s.policy("aaaa0001", 3, bodyA, time.Second), s.policy("bbbb0002", 3, bodyB, time.Second)
	one := s.ledger(append(joins, pa, pb)...)
	two := s.ledger(append(joins, pb, pa)...)
	require.Equal(t, one.View().Policy.Body, two.View().Policy.Body)
	require.Equal(t, one.View().Digest, two.View().Digest)
	require.Len(t, one.View().Rivals, 1)
	require.NotEqual(t, one.View().Policy.Body, one.View().Rivals[0].Body)
}

// A node keeps one policy per signer, the newest, so the set stays bounded
// by membership however many versions pass through.
func TestASignerKeepsOnlyItsNewestPolicy(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0))
	for v := uint64(1); v <= 5; v++ {
		_, err := l.Merge(s.policy("aaaa0001", v, bodyA+"#"+string(rune('0'+v)), time.Duration(v)*time.Second))
		require.NoError(t, err)
	}
	added, err := l.Merge(s.policy("aaaa0001", 2, bodyB, 9*time.Second))
	require.NoError(t, err)
	require.Empty(t, added, "an older version from the same signer was stored")
	n := 0
	for _, e := range l.Entries() {
		if e.Kind == Policy {
			n++
		}
	}
	require.Equal(t, 1, n)
	require.Equal(t, uint64(5), l.View().Policy.Version)
}

// Revoking the node that signed the policy in force voids it, and the
// newest policy an unrevoked node signed takes over.
func TestRevokingTheSignerFallsBackToTheNextPolicy(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.policy("aaaa0001", 1, bodyA, time.Second), s.policy("bbbb0002", 9, bodyB, 2*time.Second))
	require.Equal(t, "bbbb0002", l.View().Policy.Node)
	_, err := l.Merge(s.revoke("bbbb0002", 3*time.Second))
	require.NoError(t, err)
	v := l.View()
	require.Equal(t, "aaaa0001", v.Policy.Node)
	require.Equal(t, uint64(1), v.Policy.Version)
	require.Len(t, v.Voided, 1)
}

func TestAForgedOrUnchainedPolicyIsRefused(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	stranger := newEstate(t)
	stranger.intermediate("cccc0003")

	tampered := s.policy("aaaa0001", 1, bodyA, time.Second)
	tampered.Body = bodyB
	wrongNode := s.policy("aaaa0001", 1, bodyA, time.Second)
	wrongNode.Node = "bbbb0002"
	k := newKey(t)
	leafOnly, err := IssueNode(s.ikey["aaaa0001"], s.inter["aaaa0001"], "estate", "dddd0004", k.Public(), s.now, NodeTTL)
	require.NoError(t, err)
	byLeaf, err := sign(Entry{Kind: Policy, Troupe: "estate", Node: "dddd0004", Version: 1, By: "user:x", Body: bodyA, At: s.now.UnixMilli(), Chain: [][]byte{leafOnly.Raw}}, k)
	require.NoError(t, err)
	join := s.join("aaaa0001", "aaaa0001", 0)
	join.Body = bodyA

	for name, e := range map[string]Entry{
		"a tampered body":             tampered,
		"another node's name":         wrongNode,
		"another troupe's root":       stranger.policy("cccc0003", 1, bodyA, time.Second),
		"a leaf, not an intermediate": byLeaf,
		"version 0":                   s.policy("aaaa0001", 0, bodyA, time.Second),
		"no body":                     s.policy("aaaa0001", 1, "", time.Second),
		"a join carrying a body":      join,
	} {
		l := s.ledger()
		added, err := l.Merge(e)
		require.Error(t, err, name)
		require.Empty(t, added, name)
		require.Nil(t, l.View().Policy, name)
	}
}
