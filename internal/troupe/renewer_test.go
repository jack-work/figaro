package troupe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A renewer entry counts when the root signs it or the node's own
// intermediate does, and from no other intermediate.
func TestARenewerIsNamedByTheRootOrTheNodesOwnIntermediate(t *testing.T) {
	s := newEstate(t)
	for _, n := range []string{"a", "b", "q"} {
		s.intermediate(n)
	}
	own, err := SignOwnRenewer(s.ikey["b"], s.inter["b"], "estate", "a", s.now)
	require.NoError(t, err)
	l := s.ledger(s.join("a", "a", 0), s.join("b", "b", 0), s.join("a", "c", 0), s.renewer("c", "a"), own)
	v := l.View()
	require.Equal(t, []string{"a"}, v.Renewers["c"])
	require.Equal(t, []string{"a"}, v.Renewers["b"])
	require.True(t, v.MayIssue("c", "a"))
	require.True(t, v.MayIssue("c", "c"))
	require.False(t, v.MayIssue("c", "q"))

	forged, err := sign(Entry{Kind: Renewer, Troupe: "estate", Node: "c", Renews: "q", At: s.now.UnixMilli(), Chain: [][]byte{s.inter["q"].Raw}}, s.ikey["q"])
	require.NoError(t, err)
	tampered := s.renewer("c", "a")
	tampered.Renews = "q"
	self := s.renewer("c", "c")
	joinNamingRenewer := s.join("a", "a", time.Second)
	joinNamingRenewer.Renews = "q"
	for name, e := range map[string]Entry{
		"another intermediate":      forged,
		"a tampered renewer":        tampered,
		"the node renewing itself":  self,
		"a join carrying a renewer": joinNamingRenewer,
	} {
		_, err := l.Merge(e)
		require.Error(t, err, name)
		require.False(t, l.View().MayIssue("c", "q"), name)
	}
}

// The attack on leaving: an intermediate that is not b's mints a leaf for b
// and signs b's leave with it. b stays a member; a leave under a leaf from
// b's renewer does count.
func TestALeaveUnderALeafFromAnotherIntermediateDoesNotCount(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	s.intermediate("q")
	l := s.ledger(s.join("a", "a", 0), s.join("q", "q", 0), s.join("a", "b", 0), s.renewer("b", "a"))
	_, err := l.Merge(s.leave("b", "q", time.Minute))
	require.NoError(t, err)
	require.Contains(t, members(l.View()), "b")
	require.Empty(t, l.View().Gone["b"])

	_, err = l.Merge(s.leave("b", "a", 2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, Leave, l.View().Gone["b"])
}

// A revoked renewer renews nothing.
func TestARevokedRenewerIsDropped(t *testing.T) {
	s := newEstate(t)
	s.intermediate("a")
	s.intermediate("d")
	l := s.ledger(s.join("a", "a", 0), s.join("d", "d", 0), s.join("d", "c", 0), s.renewer("c", "a"), s.renewer("c", "d"))
	require.Equal(t, []string{"a", "d"}, l.View().Renewers["c"])
	_, err := l.Merge(s.revoke("a", time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"d"}, l.View().Renewers["c"])
}
