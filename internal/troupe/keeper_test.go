package troupe

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func keeperRig(t *testing.T, interTTL time.Duration) (*Keeper, *clock) {
	t.Helper()
	c := &clock{t: time.Now()}
	rk, err := ParseRoot(rootPEM(t))
	require.NoError(t, err)
	root, err := RootCert("estate", rk)
	require.NoError(t, err)
	ik := newKey(t)
	inter, err := IssueIntermediate(rk, root, "estate", "node-a", ik.Public(), c.t, interTTL)
	require.NoError(t, err)
	d := DirFor(t.TempDir(), "estate")
	require.NoError(t, d.WriteRoot(root))
	require.NoError(t, d.WriteIntermediate(inter, ik))
	return &Keeper{Dir: d, Troupe: "estate", Node: "node-a", Now: c.now}, c
}

// At boot the keeper issues the node certificate from the intermediate,
// and the door can serve it at once.
func TestTheKeeperIssuesTheNodeCertificateAtBoot(t *testing.T) {
	k, c := keeperRig(t, IntermediateTTL)
	require.NoError(t, k.Ensure())
	cur, err := k.Current()
	require.NoError(t, err)
	require.Len(t, cur.Certificate, 2, "the leaf travels with its intermediate")
	require.WithinDuration(t, c.t.Add(NodeTTL), cur.Leaf.NotAfter, time.Minute)
	require.Equal(t, Identity("estate", "node-a"), cur.Leaf.URIs[0].String())
}

// The certificate is renewed once less than a third of its life is left,
// and not before: a restart inside that window keeps what is on disk.
func TestTheKeeperRenewsInTheLastThird(t *testing.T) {
	k, c := keeperRig(t, IntermediateTTL)
	require.NoError(t, k.Ensure())
	first, _ := k.Current()

	c.t = c.t.Add(4 * 24 * time.Hour)
	require.NoError(t, k.Ensure())
	same, _ := k.Current()
	require.Equal(t, first.Leaf.SerialNumber, same.Leaf.SerialNumber)

	restarted := &Keeper{Dir: k.Dir, Troupe: "estate", Node: "node-a", Now: c.now}
	require.NoError(t, restarted.Ensure())
	kept, _ := restarted.Current()
	require.Equal(t, first.Leaf.SerialNumber, kept.Leaf.SerialNumber, "a restart reissued a valid certificate")

	c.t = c.t.Add(24 * time.Hour)
	require.NoError(t, k.Ensure())
	renewed, _ := k.Current()
	require.NotEqual(t, first.Leaf.SerialNumber, renewed.Leaf.SerialNumber)
	require.True(t, renewed.Leaf.NotAfter.After(first.Leaf.NotAfter))
}

// When the intermediate lapses the keeper says so, serves nothing, and the
// message names the command that fixes it.
func TestAnExpiredIntermediateLeavesNothingToServeAndSaysWhatToRun(t *testing.T) {
	k, c := keeperRig(t, 2*24*time.Hour)
	require.NoError(t, k.Ensure())
	_, err := k.Current()
	require.NoError(t, err)

	c.t = c.t.Add(3 * 24 * time.Hour)
	err = k.Ensure()
	require.True(t, errors.Is(err, ErrIntermediateExpired), "%v", err)
	require.ErrorContains(t, err, "figaro troupe intermediate estate")
	_, err = k.Current()
	require.Error(t, err, "an expired certificate was still served")

	st := k.Status()
	require.True(t, st.IntermediateExpires.Before(c.t))
	require.Error(t, st.Err)
}

// A node with no intermediate yet has nothing to serve and says how to get one.
func TestAKeeperWithNoIntermediateSaysHowToGetOne(t *testing.T) {
	k := &Keeper{Dir: DirFor(t.TempDir(), "estate"), Troupe: "estate", Node: "node-a", Now: time.Now}
	err := k.Ensure()
	require.ErrorContains(t, err, "figaro troupe intermediate estate")
	_, err = k.Current()
	require.Error(t, err)
}
