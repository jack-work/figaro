package troupe

import (
	"os"
	"path/filepath"
	"testing"

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
