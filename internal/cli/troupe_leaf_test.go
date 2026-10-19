package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/troupe"
)

const founder = "6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f"

type fakeLeafDoor struct {
	held []troupe.Entry
}

func (f *fakeLeafDoor) TroupeIdentity(context.Context, string) (*rpc.TroupeIdentityResponse, error) {
	return &rpc.TroupeIdentityResponse{Troupe: "estate", Node: founder, Principal: rpc.Principal{Kind: rpc.PrincipalUser, ID: "admin@auth.kelliher.info"}}, nil
}

func (f *fakeLeafDoor) TroupeEntries(_ context.Context, req rpc.TroupeEntriesRequest) (*rpc.TroupeEntriesResponse, error) {
	es, err := troupe.Decode(req.Entries)
	f.held = append(f.held, es...)
	return &rpc.TroupeEntriesResponse{Entries: troupe.Encode(f.held), Reached: []string{otherNode}}, err
}

// keygen on the leaf-only node, then leaf on the founder: the private key
// stays where it was made, the founder's intermediate issues a leaf for the
// public key, signs the join and has the root name the founder renewer, and
// the files for the node's troupe dir carry nothing secret.
func TestALeafIsIssuedForAKeyThatNeverLeavesItsNode(t *testing.T) {
	keys := tmpfsDir(t)
	var kout bytes.Buffer
	require.NoError(t, troupeKeygen(&kout, keyOpts{Out: keys}))
	fi, err := os.Stat(filepath.Join(keys, "node.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	pubHex := regexp.MustCompile(`public key ([0-9a-f]{64})\n`).FindStringSubmatch(kout.String())
	require.Len(t, pubHex, 2)
	nodeKey, err := os.ReadFile(filepath.Join(keys, "node.key"))
	require.NoError(t, err)
	require.NotContains(t, kout.String(), "PRIVATE KEY")

	state := t.TempDir()
	raw, _ := troupeRootPEM(t)
	_, err = troupeCreate(bytes.NewReader(raw), state, "estate", founder, time.Now())
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "for-spain")
	d := &fakeLeafDoor{}
	var msg bytes.Buffer
	o := leafOpts{For: otherNode, PublicKey: pubHex[1], Door: "10.233.1.2:7443", Out: out}
	require.NoError(t, troupeLeafFor(context.Background(), d, bytes.NewReader(raw), &msg, state, "", "estate", o, time.Now()))

	spain := troupe.Dir(out)
	block, _ := pem.Decode(nodeKey)
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	pair, err := spain.NodeWithKey(filepath.Join(keys, "node.key"))
	require.NoError(t, err, "the leaf is not for the key keygen made")
	require.True(t, k.(ed25519.PrivateKey).Public().(ed25519.PublicKey).Equal(pair.Leaf.PublicKey))
	root, err := spain.Root()
	require.NoError(t, err)
	issuer, err := x509.ParseCertificate(pair.Certificate[1])
	require.NoError(t, err)
	require.NoError(t, troupe.CheckLeaf(pair.Leaf, issuer, root, "estate", otherNode, pair.Leaf.PublicKey.(ed25519.PublicKey), time.Now()))
	require.Equal(t, founder, troupe.IntermediateNode(issuer))

	joined, err := spain.Joined()
	require.NoError(t, err)
	l := troupe.NewLedger("estate", root)
	_, err = l.Merge(joined...)
	require.NoError(t, err)
	v := l.View()
	require.True(t, v.MayIssue(otherNode, founder))
	require.Equal(t, "10.233.1.2:7443", v.Members[otherNode].Door)
	require.Equal(t, "user:admin@auth.kelliher.info", v.Members[otherNode].Owner)

	for _, f := range leafFiles {
		b, err := os.ReadFile(filepath.Join(out, f))
		require.NoError(t, err)
		require.NotContains(t, string(b), "PRIVATE KEY", f)
	}
	require.NotContains(t, msg.String(), hex.EncodeToString(raw))
	require.Contains(t, msg.String(), "which the root now names its renewer")
	var rawEntries []json.RawMessage
	b, _ := os.ReadFile(filepath.Join(out, "joined.json"))
	require.NoError(t, json.Unmarshal(b, &rawEntries))
	require.Len(t, rawEntries, 2)
}

// What is not a node id, an Ed25519 key, a dialable door or this troupe's
// root is refused before anything is signed or written.
func TestALeafRefusesWhatItCannotStandBehind(t *testing.T) {
	state := t.TempDir()
	raw, _ := troupeRootPEM(t)
	_, err := troupeCreate(bytes.NewReader(raw), state, "estate", founder, time.Now())
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	good := leafOpts{For: otherNode, PublicKey: hex.EncodeToString(pub), Door: "10.233.1.2:7443"}
	other, _ := troupeRootPEM(t)
	for name, c := range map[string]struct {
		o    leafOpts
		root []byte
		want string
	}{
		"a bad node id":    {leafOpts{For: "spain", PublicKey: good.PublicKey, Door: good.Door}, raw, "not a node id"},
		"a short key":      {leafOpts{For: otherNode, PublicKey: "abcd", Door: good.Door}, raw, "--public-key"},
		"a wildcard door":  {leafOpts{For: otherNode, PublicKey: good.PublicKey, Door: "0.0.0.0:7443"}, raw, "--door"},
		"another root":     {good, other, "not troupe estate's root"},
		"this node itself": {leafOpts{For: founder, PublicKey: good.PublicKey, Door: good.Door}, raw, "names this node"},
	} {
		d := &fakeLeafDoor{}
		c.o.Out = filepath.Join(t.TempDir(), "out")
		err := troupeLeafFor(context.Background(), d, bytes.NewReader(c.root), &bytes.Buffer{}, state, "", "estate", c.o, time.Now())
		require.ErrorContains(t, err, c.want, name)
		require.Empty(t, d.held, name)
		left, _ := os.ReadDir(c.o.Out)
		require.Empty(t, left, name)
	}
}
