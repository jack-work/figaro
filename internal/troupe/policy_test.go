package troupe

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
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

func setters(body string, nodes ...string) string {
	q := make([]string, len(nodes))
	for i, n := range nodes {
		q[i] = fmt.Sprintf("%q", n)
	}
	return "setters = [" + strings.Join(q, ", ") + "]\n" + body
}

func (s *estate) rootPolicy(version uint64, body string, at time.Duration) Entry {
	s.t.Helper()
	e, err := SignRootPolicy(s.rk, "estate", version, "root:estate", body, s.now.Add(at))
	require.NoError(s.t, err)
	return e
}

// v1 comes from the root and names the setters; a setter's intermediate
// carries the policy on from there, and the highest such version is in force.
func TestTheRootStartsThePolicyAndItsSettersCarryItOn(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.rootPolicy(1, setters(bodyA, "aaaa0001"), time.Second),
		s.policy("aaaa0001", 2, setters(bodyB, "aaaa0001", "bbbb0002"), 2*time.Second),
		s.policy("bbbb0002", 3, setters(bodyA, "bbbb0002"), 3*time.Second))
	v := l.View()
	require.NotNil(t, v.Policy)
	require.Equal(t, uint64(3), v.Policy.Version)
	require.Equal(t, "bbbb0002", v.Policy.Node)
	require.Empty(t, v.Rivals)
	require.Empty(t, v.Refused)
}

// The attack: a second intermediate, which no version names, publishes a
// policy. Before the root's v1 it is not adopted, and after it, at any
// version, it is still not adopted.
func TestAnIntermediateNoVersionNamesCannotPublish(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("eeee0666")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("eeee0666", "eeee0666", 0))

	_, err := l.Merge(s.policy("eeee0666", 1, setters(bodyB, "eeee0666"), time.Second))
	require.ErrorContains(t, err, "names that node a setter")
	require.Nil(t, l.View().Policy, "an intermediate started the troupe policy")

	_, err = l.Merge(s.rootPolicy(1, setters(bodyA, "aaaa0001"), 2*time.Second))
	require.NoError(t, err)
	_, err = l.Merge(s.policy("eeee0666", 99, setters(bodyB, "eeee0666"), 3*time.Second))
	require.ErrorContains(t, err, "names that node a setter")
	v := l.View()
	require.Equal(t, uint64(1), v.Policy.Version)
	require.True(t, v.Policy.RootSigned())
	require.Equal(t, bodyA, v.Policy.Body[len(v.Policy.Body)-len(bodyA):])
}

// A setter dropped from the list cannot come back with a higher version,
// however its entries arrive: each version answers to the one adopted before.
func TestASetterDroppedFromTheListIsRefusedAfterward(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	es := []Entry{
		s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.rootPolicy(1, setters(bodyA, "aaaa0001", "bbbb0002"), time.Second),
		s.policy("bbbb0002", 2, setters(bodyB, "bbbb0002"), 2*time.Second),
		s.policy("aaaa0001", 3, setters(bodyA, "aaaa0001"), 3*time.Second),
	}
	for range 10 {
		rand.Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
		l := NewLedger("estate", s.root)
		_, _ = l.Merge(es...)
		v := l.View()
		require.Equal(t, uint64(2), v.Policy.Version)
		require.Equal(t, "bbbb0002", v.Policy.Node)
		require.Len(t, v.Refused, 1)
		require.Equal(t, "aaaa0001", v.Refused[0].Node)
	}
}

// The root may sign any version, and a root version names a new list.
func TestTheRootCanTakeThePolicyBackAtAnyVersion(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.rootPolicy(1, setters(bodyA, "aaaa0001"), time.Second),
		s.policy("aaaa0001", 2, setters(bodyA, "aaaa0001"), 2*time.Second),
		s.rootPolicy(3, setters(bodyB, "bbbb0002"), 3*time.Second),
		s.policy("bbbb0002", 4, setters(bodyB, "bbbb0002"), 4*time.Second))
	_, err := l.Merge(s.policy("aaaa0001", 5, setters(bodyA, "aaaa0001"), 5*time.Second))
	require.NoError(t, err, "a node once named a setter keeps its entries stored")
	v := l.View()
	require.Equal(t, uint64(4), v.Policy.Version)
	require.Equal(t, "bbbb0002", v.Policy.Node)
	require.Len(t, v.Refused, 1)
}

// Two setters writing the same version converge everywhere on one of them,
// and the other is kept visible rather than silently lost.
func TestTwoWritesAtOneVersionConvergeAndTheLoserIsNamed(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	base := []Entry{s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.rootPolicy(1, setters(bodyA, "aaaa0001", "bbbb0002"), 0)}
	pa, pb := s.policy("aaaa0001", 3, bodyA, time.Second), s.policy("bbbb0002", 3, bodyB, time.Second)
	one := s.ledger(append(slices.Clone(base), pa, pb)...)
	two := s.ledger(append(slices.Clone(base), pb, pa)...)
	require.Equal(t, one.View().Policy.Body, two.View().Policy.Body)
	require.Equal(t, one.View().Digest, two.View().Digest)
	require.Len(t, one.View().Rivals, 1)
	require.NotEqual(t, one.View().Policy.Body, one.View().Rivals[0].Body)
}

// A setter and the root writing the same version: the root's entry is in
// force whatever the bodies hash to and whatever time the setter signed,
// and the setter's is kept as the rival.
func TestTheRootWinsItsOwnVersionOutright(t *testing.T) {
	for _, bodies := range [][2]string{{bodyA, bodyB}, {bodyB, bodyA}} {
		s := newEstate(t)
		s.intermediate("aaaa0001")
		rootBody, setterBody := setters(bodies[0], "aaaa0001"), setters(bodies[1], "aaaa0001")
		base := []Entry{s.join("aaaa0001", "aaaa0001", 0), s.rootPolicy(1, rootBody, 0)}
		root := s.rootPolicy(2, rootBody, time.Second)
		setter := s.policy("aaaa0001", 2, setterBody, time.Hour)
		for _, es := range [][]Entry{{root, setter}, {setter, root}} {
			v := s.ledger(append(slices.Clone(base), es...)...).View()
			require.Equal(t, uint64(2), v.Policy.Version)
			require.True(t, v.Policy.RootSigned())
			require.Equal(t, rootBody, v.Policy.Body)
			require.Len(t, v.Rivals, 1)
			require.Equal(t, "aaaa0001", v.Rivals[0].Node)
		}
	}
}

// Revoking the node that signed the policy in force voids it, and the
// version before it, which the root signed, is in force again.
func TestRevokingTheSignerFallsBackToTheNextPolicy(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	l := s.ledger(s.join("aaaa0001", "aaaa0001", 0), s.join("bbbb0002", "bbbb0002", 0),
		s.rootPolicy(1, setters(bodyA, "bbbb0002"), time.Second), s.policy("bbbb0002", 9, bodyB, 2*time.Second))
	require.Equal(t, "bbbb0002", l.View().Policy.Node)
	_, err := l.Merge(s.revoke("bbbb0002", 3*time.Second))
	require.NoError(t, err)
	v := l.View()
	require.True(t, v.Policy.RootSigned())
	require.Equal(t, uint64(1), v.Policy.Version)
	require.Len(t, v.Voided, 1)
}

func TestAForgedOrUnchainedPolicyIsRefused(t *testing.T) {
	s := newEstate(t)
	s.intermediate("aaaa0001")
	s.intermediate("bbbb0002")
	named := s.rootPolicy(1, setters(bodyA, "aaaa0001", "bbbb0002", "cccc0003", "dddd0004"), 0)
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

	rootNamed := s.rootPolicy(1, bodyA, time.Second)
	rootNamed.Node = "aaaa0001"
	badSetters := s.rootPolicy(1, "setters = \"aaaa0001\"\n"+bodyA, time.Second)
	forgedRoot := s.rootPolicy(1, bodyA, time.Second)
	forgedRoot.Body = bodyB

	for name, e := range map[string]Entry{
		"a root policy naming a node": rootNamed,
		"setters that are not a list": badSetters,
		"a tampered root policy":      forgedRoot,
		"a tampered body":             tampered,
		"another node's name":         wrongNode,
		"another troupe's root":       stranger.policy("cccc0003", 1, bodyA, time.Second),
		"a leaf, not an intermediate": byLeaf,
		"version 0":                   s.policy("aaaa0001", 0, bodyA, time.Second),
		"no body":                     s.policy("aaaa0001", 1, "", time.Second),
		"a join carrying a body":      join,
	} {
		l := s.ledger()
		if !e.RootSigned() {
			l = s.ledger(named)
		}
		added, err := l.Merge(e)
		require.Error(t, err, name)
		require.Empty(t, added, name)
		if e.RootSigned() {
			require.Nil(t, l.View().Policy, name)
		} else {
			require.Equal(t, named.ID(), l.View().Policy.ID(), name)
		}
	}
}
