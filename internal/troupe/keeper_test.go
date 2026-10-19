package troupe

import (
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"sync"
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

type leafRig struct {
	k      *Keeper
	c      *clock
	signer *Keeper
	asked  int
}

// leafRig is node-b holding only a leaf that node-a's intermediate issued,
// as a joined node does, renewing through the signer it is given.
func newLeafRig(t *testing.T) *leafRig {
	t.Helper()
	signer, c := keeperRig(t, IntermediateTTL)
	root, err := signer.Dir.Root()
	require.NoError(t, err)
	inter, ik, err := signer.Dir.Intermediate()
	require.NoError(t, err)
	nk := newKey(t)
	leaf, err := IssueNode(ik, inter, "estate", "node-b", nk.Public(), c.t, NodeTTL)
	require.NoError(t, err)
	d := DirFor(t.TempDir(), "estate")
	require.NoError(t, d.WriteRoot(root))
	require.NoError(t, d.WriteNode(leaf, inter, nk))
	r := &leafRig{c: c, signer: signer}
	r.k = &Keeper{Dir: d, Troupe: "estate", Node: "node-b", Now: c.now, Renew: func(pub ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
		r.asked++
		leaf, err := IssueNode(ik, inter, "estate", "node-b", pub, c.t, NodeTTL)
		return leaf, inter, err
	}}
	return r
}

// A node with a leaf and no intermediate serves the leaf, and asks a
// signer for a new one only in its last third.
func TestALeafOnlyNodeRenewsFromASignerInTheLastThird(t *testing.T) {
	r := newLeafRig(t)
	require.NoError(t, r.k.Ensure())
	first, err := r.k.Current()
	require.NoError(t, err)
	require.Zero(t, r.asked)
	require.True(t, r.k.Status().IntermediateExpires.IsZero())

	r.c.t = r.c.t.Add(5 * 24 * time.Hour)
	require.NoError(t, r.k.Ensure())
	require.Equal(t, 1, r.asked)
	renewed, err := r.k.Current()
	require.NoError(t, err)
	require.NotEqual(t, first.Leaf.SerialNumber, renewed.Leaf.SerialNumber)
	held, err := r.k.Dir.Node()
	require.NoError(t, err)
	require.Equal(t, renewed.Leaf.SerialNumber, held.Leaf.SerialNumber, "the renewal was not written")
}

// A renewal that fails keeps the current leaf while it lasts, and once it
// expires there is nothing to serve and the remedy is to join again.
func TestALeafThatCannotBeRenewedIsServedUntilItExpires(t *testing.T) {
	r := newLeafRig(t)
	r.k.Renew = func(ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
		return nil, nil, errors.New("node-a unreachable")
	}
	r.c.t = r.c.t.Add(6 * 24 * time.Hour)
	err := r.k.Ensure()
	require.ErrorContains(t, err, "node-a unreachable")
	_, err = r.k.Current()
	require.NoError(t, err, "a valid leaf stopped being served")

	r.c.t = r.c.t.Add(2 * 24 * time.Hour)
	err = r.k.Ensure()
	require.ErrorContains(t, err, "figaro troupe join estate")
	_, err = r.k.Current()
	require.Error(t, err)
}

// A signer cannot hand a node a certificate for another key or another
// node, nor one from outside the troupe.
func TestARenewalIsCheckedBeforeItIsKept(t *testing.T) {
	other := newKey(t)
	cases := map[string]func(r *leafRig) Renew{
		"another key": func(r *leafRig) Renew {
			inter, ik, _ := r.signer.Dir.Intermediate()
			return func(ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
				leaf, err := IssueNode(ik, inter, "estate", "node-b", other.Public(), r.c.t, NodeTTL)
				return leaf, inter, err
			}
		},
		"another node": func(r *leafRig) Renew {
			inter, ik, _ := r.signer.Dir.Intermediate()
			return func(pub ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
				leaf, err := IssueNode(ik, inter, "estate", "node-c", pub, r.c.t, NodeTTL)
				return leaf, inter, err
			}
		},
		"another root": func(r *leafRig) Renew {
			stranger, _ := keeperRig(t, IntermediateTTL)
			inter, ik, _ := stranger.Dir.Intermediate()
			return func(pub ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
				leaf, err := IssueNode(ik, inter, "estate", "node-b", pub, r.c.t, NodeTTL)
				return leaf, inter, err
			}
		},
	}
	for name, bad := range cases {
		r := newLeafRig(t)
		require.NoError(t, r.k.Ensure())
		first, _ := r.k.Current()
		r.k.Renew = bad(r)
		r.c.t = r.c.t.Add(5 * 24 * time.Hour)
		require.Error(t, r.k.Ensure(), name)
		cur, err := r.k.Current()
		require.NoError(t, err, name)
		require.Equal(t, first.Leaf.SerialNumber, cur.Leaf.SerialNumber, name)
	}
}

// The daemon renews at start while the hourly check may run too: two
// renewals at once must not leave a key beside another renewal's
// certificate.
func TestConcurrentEnsuresRenewOnceAndNoneFails(t *testing.T) {
	r := newLeafRig(t)
	r.c.t = r.c.t.Add(5 * 24 * time.Hour)
	errs := make([]error, 16)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.k.Ensure()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, r.asked, "one renewal, the rest found it fresh")
	_, err := r.k.Dir.Node()
	require.NoError(t, err)
}
