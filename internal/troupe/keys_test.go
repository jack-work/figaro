package troupe

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func rootPEM(t *testing.T) []byte {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return k
}

// The root is held in 1Password, not on any node, so every holder of the
// key must derive the same root certificate from it.
func TestTheRootCertificateIsAFunctionOfTheKey(t *testing.T) {
	raw := rootPEM(t)
	k1, err := ParseRoot(raw)
	require.NoError(t, err)
	k2, err := ParseRoot(bytes.Clone(raw))
	require.NoError(t, err)
	c1, err := RootCert("estate", k1)
	require.NoError(t, err)
	c2, err := RootCert("estate", k2)
	require.NoError(t, err)
	require.Equal(t, c1.Raw, c2.Raw)
	require.True(t, c1.IsCA)

	other, err := RootCert("elsewhere", k1)
	require.NoError(t, err)
	require.NotEqual(t, c1.Raw, other.Raw, "the troupe name is part of the root")
}

func TestParseRootRefusesWhatIsNotAnEd25519Key(t *testing.T) {
	for _, in := range []string{"", "not pem", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}))} {
		_, err := ParseRoot([]byte(in))
		require.Error(t, err, "%q", in)
	}
}

func TestTroupeNamesAreHostLabels(t *testing.T) {
	for _, ok := range []string{"estate", "gluck-home", "a1"} {
		require.NoError(t, ValidName(ok), ok)
	}
	for _, bad := range []string{"", "Estate", "a/b", "a.b", "-x", strings.Repeat("a", 64)} {
		require.Error(t, ValidName(bad), bad)
	}
}

// root -> intermediate -> node: a node certificate verifies against the
// root alone, carries the node's identity, and serves both ends of the door.
func TestANodeCertificateChainsToTheRoot(t *testing.T) {
	now := time.Now()
	rk, err := ParseRoot(rootPEM(t))
	require.NoError(t, err)
	root, err := RootCert("estate", rk)
	require.NoError(t, err)
	ik := newKey(t)
	inter, err := IssueIntermediate(rk, root, "estate", "node-a", ik.Public(), now, IntermediateTTL)
	require.NoError(t, err)
	require.True(t, inter.IsCA)
	require.True(t, inter.MaxPathLenZero)

	nk := newKey(t)
	leaf, err := IssueNode(ik, inter, "estate", "node-a", nk.Public(), now, NodeTTL)
	require.NoError(t, err)
	require.Equal(t, Identity("estate", "node-a"), leaf.URIs[0].String())
	require.WithinDuration(t, now.Add(NodeTTL), leaf.NotAfter, time.Minute)

	roots := x509.NewCertPool()
	roots.AddCert(root)
	inters := x509.NewCertPool()
	inters.AddCert(inter)
	for _, use := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, KeyUsages: []x509.ExtKeyUsage{use}, CurrentTime: now})
		require.NoError(t, err)
	}
}

// An intermediate signs only for its own troupe and only leaves: a stolen
// one cannot mint another troupe's nodes or a further CA.
func TestAnIntermediateIsConfinedToItsTroupeAndToLeaves(t *testing.T) {
	now := time.Now()
	rk, _ := ParseRoot(rootPEM(t))
	root, _ := RootCert("estate", rk)
	ik := newKey(t)
	inter, err := IssueIntermediate(rk, root, "estate", "node-a", ik.Public(), now, IntermediateTTL)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inters := x509.NewCertPool()
	inters.AddCert(inter)

	foreign, err := IssueNode(ik, inter, "elsewhere", "node-x", newKey(t).Public(), now, NodeTTL)
	require.NoError(t, err)
	_, err = foreign.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, CurrentTime: now})
	require.Error(t, err, "a node of another troupe verified")
}

// A node certificate never outlives the intermediate that signed it.
func TestANodeCertificateEndsWithItsIntermediate(t *testing.T) {
	now := time.Now()
	rk, _ := ParseRoot(rootPEM(t))
	root, _ := RootCert("estate", rk)
	ik := newKey(t)
	inter, err := IssueIntermediate(rk, root, "estate", "node-a", ik.Public(), now, 48*time.Hour)
	require.NoError(t, err)
	leaf, err := IssueNode(ik, inter, "estate", "node-a", newKey(t).Public(), now, NodeTTL)
	require.NoError(t, err)
	require.False(t, leaf.NotAfter.After(inter.NotAfter))

	_, err = IssueNode(ik, inter, "estate", "node-a", newKey(t).Public(), now.Add(72*time.Hour), NodeTTL)
	require.ErrorIs(t, err, ErrIntermediateExpired)
}
