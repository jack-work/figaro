package troupe

import (
	"crypto/ed25519"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

// An issuer grants what was asked, up to its own most, and nothing asked
// means the old seven days.
func TestAGrantIsTheAskCappedByTheIssuersMost(t *testing.T) {
	cases := []struct {
		name        string
		asked, most time.Duration
		want        time.Duration
	}{
		{"nothing asked", 0, 0, NodeTTL},
		{"a negative ask is no ask", -day, 0, NodeTTL},
		{"30d under the default most", 30 * day, 0, 30 * day},
		{"60d capped at the default most", 60 * day, 0, MaxNodeTTL},
		{"30d capped at an issuer's 10d", 30 * day, 10 * day, 10 * day},
		{"3d under an issuer's 10d", 3 * day, 10 * day, 3 * day},
		{"1ms floored at a day", time.Millisecond, 0, MinNodeTTL},
		{"1h floored at a day under an issuer's 10d", time.Hour, 10 * day, MinNodeTTL},
	}
	for _, c := range cases {
		require.Equal(t, c.want, GrantNodeTTL(c.asked, c.most), c.name)
	}
}

func TestANodeTTLIsDaysOrADurationOfAtLeastADay(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * day, "1d": day, "36h": 36 * time.Hour, "3650d": 3650 * day} {
		got, err := ParseNodeTTL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "12h", "0d", "-3d", "thirty", "1.5d", "30", "3651d", "87601h", "106752d", "213505d", "9223372036854775807d"} {
		_, err := ParseNodeTTL(in)
		require.Error(t, err, in)
	}
	_, err := ParseNodeTTL("12h")
	require.ErrorContains(t, err, "at least 1d")
	require.Equal(t, "30d", FormatNodeTTL(30*day))
	require.Equal(t, "36h0m0s", FormatNodeTTL(36*time.Hour))
}

// A node that holds an intermediate issues its own leaf for its leaf_ttl,
// capped by its own max_leaf_ttl and by the intermediate's end.
func TestASelfIssuedLeafTakesTheConfiguredLifeUnderBothCaps(t *testing.T) {
	cases := []struct {
		name         string
		ttl, most    time.Duration
		interTTL     time.Duration
		wantLifetime time.Duration
	}{
		{"unset", 0, 0, IntermediateTTL, NodeTTL},
		{"30d", 30 * day, 0, IntermediateTTL, 30 * day},
		{"60d asked, 30d most", 60 * day, 0, IntermediateTTL, MaxNodeTTL},
		{"30d asked, 14d most", 30 * day, 14 * day, IntermediateTTL, 14 * day},
		{"30d asked, intermediate ends in 10d", 30 * day, 0, 10 * day, 10 * day},
	}
	for _, c := range cases {
		k, clk := keeperRig(t, c.interTTL)
		k.TTL, k.MaxTTL = c.ttl, c.most
		require.NoError(t, k.Ensure(), c.name)
		cur, err := k.Current()
		require.NoError(t, err, c.name)
		require.WithinDuration(t, clk.t.Add(c.wantLifetime), cur.Leaf.NotAfter, time.Minute, c.name)
	}
}

// longLeafRig is a leaf-only node configured for 30d leaves, holding one
// that its signer granted for 30d, renewing through a signer that grants
// what it is asked.
func longLeafRig(t *testing.T) (*leafRig, *[]time.Duration) {
	t.Helper()
	r := newLeafRig(t)
	inter, ik, err := r.signer.Dir.Intermediate()
	require.NoError(t, err)
	_, nk, err := r.k.nodeKey()
	require.NoError(t, err)
	leaf, err := IssueNode(ik, inter, "estate", "node-b", nk.Public(), r.c.t, 30*day)
	require.NoError(t, err)
	require.NoError(t, r.k.Dir.WriteNode(leaf, inter, nk))
	var asked []time.Duration
	r.k.TTL = 30 * day
	r.k.Renew = func(pub ed25519.PublicKey, ttl time.Duration) (*x509.Certificate, *x509.Certificate, error) {
		r.asked++
		asked = append(asked, ttl)
		leaf, err := IssueNode(ik, inter, "estate", "node-b", pub, r.c.t, GrantNodeTTL(ttl, 0))
		return leaf, inter, err
	}
	return r, &asked
}

// Renewal stays at a third of the life left: a 30d leaf is kept through
// day 19 and renewed from day 20, for another 30d, asked for by the node.
func TestAThirtyDayLeafRenewsFromDayTwenty(t *testing.T) {
	r, asked := longLeafRig(t)
	start := r.c.t
	require.NoError(t, r.k.Ensure())
	first, err := r.k.Current()
	require.NoError(t, err)

	for _, d := range []time.Duration{day, 10 * day, 19 * day, 19*day + 23*time.Hour} {
		r.c.t = start.Add(d)
		require.NoError(t, r.k.Ensure(), "day %v", d)
		require.Zero(t, r.asked, "renewed at %v, before the last third", d)
	}

	r.c.t = start.Add(20*day + time.Minute)
	require.NoError(t, r.k.Ensure())
	require.Equal(t, 1, r.asked)
	require.Equal(t, []time.Duration{30 * day}, *asked, "the node asked for its leaf_ttl")
	renewed, err := r.k.Current()
	require.NoError(t, err)
	require.NotEqual(t, first.Leaf.SerialNumber, renewed.Leaf.SerialNumber)
	require.WithinDuration(t, r.c.t.Add(30*day), renewed.Leaf.NotAfter, time.Minute)
}

// A renewer that predates the field ignores it and grants seven days. The
// node keeps that leaf, and renews it at a third of its own life.
func TestALeafFromARenewerThatIgnoresTheAskIsKept(t *testing.T) {
	r, _ := longLeafRig(t)
	inter, ik, err := r.signer.Dir.Intermediate()
	require.NoError(t, err)
	r.k.Renew = func(pub ed25519.PublicKey, _ time.Duration) (*x509.Certificate, *x509.Certificate, error) {
		r.asked++
		leaf, err := IssueNode(ik, inter, "estate", "node-b", pub, r.c.t, NodeTTL)
		return leaf, inter, err
	}
	r.c.t = r.c.t.Add(20*day + time.Minute)
	require.NoError(t, r.k.Ensure())
	cur, err := r.k.Current()
	require.NoError(t, err)
	require.WithinDuration(t, r.c.t.Add(NodeTTL), cur.Leaf.NotAfter, time.Minute)

	r.c.t = r.c.t.Add(4 * day)
	require.NoError(t, r.k.Ensure())
	require.Equal(t, 1, r.asked, "a 7d leaf was renewed before its last third")
	r.c.t = r.c.t.Add(day)
	require.NoError(t, r.k.Ensure())
	require.Equal(t, 2, r.asked)
}
