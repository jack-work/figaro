package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
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

// newCredentialLeafNode is spain: a node that holds no intermediate, whose
// key is a credential it generated, with a leaf signer's intermediate
// issued for that key, the entries signer's leaf command hands over, and no
// key in its troupe dir.
func newCredentialLeafNode(t *testing.T, root estateRoot, signer *ledgerNode, issued time.Time) *ledgerNode {
	t.Helper()
	n := &ledgerNode{t: t, root: root, storeDir: t.TempDir(), stateDir: t.TempDir(), cfgDir: t.TempDir(), addr: freeAddr(t)}
	body := fmt.Sprintf("[troupe]\nname = \"estate\"\nprovider = \"figaro\"\nnode_key_credential = \"troupe-node\"\n[door.tls]\nlisten = %q\n", n.addr)
	require.NoError(t, os.WriteFile(filepath.Join(n.cfgDir, "config.toml"), []byte(body), 0o600))
	creds := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(creds, "troupe-node"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))

	n.open()
	inter, ik, err := signer.dir().Intermediate()
	require.NoError(t, err)
	leaf, err := troupe.IssueNode(ik, inter, "estate", n.id(), pub, issued, troupe.NodeTTL)
	require.NoError(t, err)
	require.NoError(t, n.dir().WriteRoot(root.cert))
	require.NoError(t, n.dir().WriteNodeCert(leaf, inter))
	n.backend.Close()
	return n
}

// spain end to end: the founder records spain's join and the root's renewer
// entry; spain starts with its credential key, the founder admits it over
// TLS, and spain renews from the founder for the same key, writing only the
// certificate.
func TestALeafOnlyNodeWithACredentialKeyJoinsAndRenews(t *testing.T) {
	root := newEstateRoot(t)
	na := newLedgerNode(t, root)
	ns := newCredentialLeafNode(t, root, na, time.Now().Add(-6*24*time.Hour))
	ma := na.start()
	_, err := ma.Merge(ns.joinedBy(na)...)
	require.NoError(t, err)
	require.NoError(t, ns.dir().WriteJoined(ma.Entries()))

	ms := ns.start()
	_, atRest := ns.dir().NodeKeyAtRest()
	require.False(t, atRest)
	first, err := ns.a.Troupe.Current()
	require.NoError(t, err)

	l, err := (&peer.Dialer{Members: ma, Versions: &peer.Versions{}, Timeout: 2 * time.Second}).Open(context.Background(), ns.id(), rpc.Passage{}, nil)
	require.NoError(t, err, "the founder did not admit spain's leaf")
	l.Close()
	_, err = ms.Merge(ma.Entries()...)
	require.NoError(t, err)

	require.NoError(t, ns.a.Troupe.Ensure())
	renewed, err := ns.a.Troupe.Current()
	require.NoError(t, err)
	require.NotEqual(t, first.Leaf.SerialNumber, renewed.Leaf.SerialNumber, "nothing was renewed")
	require.True(t, first.Leaf.PublicKey.(ed25519.PublicKey).Equal(renewed.Leaf.PublicKey), "the renewal changed the key")
	_, atRest = ns.dir().NodeKeyAtRest()
	require.False(t, atRest, "a key landed in spain's troupe dir")
}
