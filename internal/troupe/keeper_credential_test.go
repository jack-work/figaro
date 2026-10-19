package troupe

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// credentialRig moves the intermediate key out of the troupe dir into a
// systemd-style credential, leaving only the certificates at rest.
func credentialRig(t *testing.T) (*Keeper, string) {
	t.Helper()
	k, _ := keeperRig(t, IntermediateTTL)
	cred := filepath.Join(t.TempDir(), "troupe-intermediate")
	key, ok := k.Dir.IntermediateKeyAtRest()
	require.True(t, ok)
	require.NoError(t, os.Rename(key, cred))
	k.IntermediateKey = cred
	return k, cred
}

// With the key in a credential and none in the troupe dir, the node still
// issues its own certificate.
func TestTheIntermediateKeyCanComeFromACredential(t *testing.T) {
	k, _ := credentialRig(t)
	_, atRest := k.Dir.IntermediateKeyAtRest()
	require.False(t, atRest)
	require.NoError(t, k.Ensure())
	cur, err := k.Current()
	require.NoError(t, err)
	require.Equal(t, Identity("estate", "node-a"), cur.Leaf.URIs[0].String())
	require.False(t, k.Status().IntermediateExpires.IsZero())
}

// A configured credential that is missing is an error, never a quiet turn to
// the leaf-only path, and never a read of a key left in the troupe dir.
func TestAMissingCredentialIsAnErrorNotAFallback(t *testing.T) {
	k, cred := credentialRig(t)
	inter, ikey, err := k.Dir.IntermediateWithKey(cred)
	require.NoError(t, err)
	require.NoError(t, k.Dir.WriteIntermediate(inter, ikey))
	require.NoError(t, os.Remove(cred))

	err = k.Ensure()
	require.ErrorContains(t, err, "the intermediate from the credential")
	require.ErrorContains(t, err, "--for node-a", "the remedy issues on the founder")
	_, err = k.Current()
	require.Error(t, err)
}

// A credential holding some other key is refused: the node would otherwise
// issue certificates its intermediate does not vouch for.
func TestACredentialThatIsNotTheCertifiedKeyIsRefused(t *testing.T) {
	k, cred := credentialRig(t)
	other, err := keyPEM(newKey(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cred, other, 0o600))
	require.ErrorContains(t, k.Ensure(), "does not hold the key")
}

// A leaf-only node whose key is a credential serves its certificate,
// renews it from its signer for the same key, and writes only the
// certificate back: the key never lands in the troupe dir.
func TestALeafOnlyNodeKeepsItsCredentialKeyThroughARenewal(t *testing.T) {
	r := newLeafRig(t)
	cred := filepath.Join(t.TempDir(), "troupe-node")
	key, ok := r.k.Dir.NodeKeyAtRest()
	require.True(t, ok)
	require.NoError(t, os.Rename(key, cred))
	r.k.NodeKey = cred

	require.NoError(t, r.k.Ensure())
	first, err := r.k.Current()
	require.NoError(t, err)
	require.Zero(t, r.asked)

	r.c.t = r.c.t.Add(5 * 24 * time.Hour)
	require.NoError(t, r.k.Ensure())
	require.Equal(t, 1, r.asked)
	renewed, err := r.k.Current()
	require.NoError(t, err)
	require.NotEqual(t, first.Leaf.SerialNumber, renewed.Leaf.SerialNumber)
	require.True(t, first.Leaf.PublicKey.(ed25519.PublicKey).Equal(renewed.Leaf.PublicKey), "the renewal changed the key")
	_, atRest := r.k.Dir.NodeKeyAtRest()
	require.False(t, atRest, "the renewal wrote a key into the troupe dir")
}

// A node key credential that does not match the node certificate is an
// error that says how to get a new certificate, not a quiet rejoin.
func TestANodeKeyCredentialThatIsNotTheCertifiedKeyIsRefused(t *testing.T) {
	r := newLeafRig(t)
	cred := filepath.Join(t.TempDir(), "troupe-node")
	other, err := keyPEM(newKey(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cred, other, 0o600))
	r.k.NodeKey = cred
	err = r.k.Ensure()
	require.ErrorContains(t, err, "with the key from the credential")
	require.ErrorContains(t, err, "figaro troupe leaf estate --for node-b")
}
