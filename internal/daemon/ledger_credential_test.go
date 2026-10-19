package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

// readsKeyFromCredential moves n's intermediate key out of its troupe dir
// into a credentials directory, as systemd's LoadCredential delivers it, and
// names it in [troupe].
func (n *ledgerNode) readsKeyFromCredential() {
	t := n.t
	creds := t.TempDir()
	key, ok := n.dir().IntermediateKeyAtRest()
	require.True(t, ok)
	require.NoError(t, os.Rename(key, filepath.Join(creds, "troupe-intermediate")))
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	path := filepath.Join(n.cfgDir, "config.toml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	body := strings.Replace(string(raw), "provider = \"figaro\"\n", "provider = \"figaro\"\nintermediate_key_credential = \"troupe-intermediate\"\n", 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// A node whose intermediate key is a credential signs itself in and signs
// a joined node's renewal, with no key in its troupe dir.
func TestANodeSignsWithAnIntermediateKeyFromACredential(t *testing.T) {
	root := newEstateRoot(t)
	na := newLedgerNode(t, root)
	nb := newLeafNode(t, root, na, time.Now().Add(-6*24*time.Hour))
	join := nb.joinedBy(na)
	na.readsKeyFromCredential()

	ma := na.start()
	_, atRest := na.dir().IntermediateKeyAtRest()
	require.False(t, atRest)
	me, ok := ma.View().Members[na.id()]
	require.True(t, ok, "the founder did not sign itself in")
	require.Equal(t, na.addr, me.Door)

	_, err := ma.Merge(join...)
	require.NoError(t, err)
	mb := nb.start()
	_, err = mb.Merge(ma.Entries()...)
	require.NoError(t, err)
	require.NoError(t, nb.a.Troupe.Ensure())
	require.True(t, nb.a.Troupe.Status().NodeExpires.After(time.Now().Add(6*24*time.Hour)), "the renewal was not signed")
}

// Naming a credential with no credentials directory stops federation with
// an error that names the setting, rather than reading the troupe dir.
func TestACredentialWithoutACredentialsDirectoryIsRefused(t *testing.T) {
	n := newLedgerNode(t, newEstateRoot(t))
	n.readsKeyFromCredential()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	n.open()
	defer n.backend.Close()
	loaded, err := config.Load(n.cfgDir)
	require.NoError(t, err)
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: n.backend})
	a.Peercred, err = authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	require.NoError(t, err)
	err = federateIn(context.Background(), a, loaded, n.stateDir)
	require.ErrorContains(t, err, "troupe.intermediate_key_credential")
	require.ErrorContains(t, err, "CREDENTIALS_DIRECTORY")
}
