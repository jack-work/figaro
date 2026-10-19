package angelus

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/jkauthz"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
)

const troupeJoinPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[[bind]]
role = "owner"
principals = ["user:0"]

[roles.member]
grants = [{ verbs = ["join"], on = ["troupe:estate"] }]
[[bind]]
role = "member"
principals = ["user:member"]

[roles.reader]
grants = [{ verbs = ["read"], on = ["*"] }]
[[bind]]
role = "reader"
principals = ["user:reader"]
`

type signRig struct {
	t    *testing.T
	a    *Angelus
	call map[string]func(context.Context, json.RawMessage) (any, error)
	rk   ed25519.PrivateKey
	root *x509.Certificate
}

// newSignRig is a node of troupe estate; with intermediate it can sign
// others in, without it it holds only a leaf another node issued.
func newSignRig(t *testing.T, intermediate bool) *signRig {
	t.Helper()
	b := store.NewTestBackend(t)
	a := &Angelus{Registry: NewRegistry(), Backend: b}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(b))
	path := filepath.Join(t.TempDir(), "policy.toml")
	require.NoError(t, os.WriteFile(path, []byte(troupeJoinPolicy), 0o600))
	require.NoError(t, a.Policies.Reload(path))

	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := troupe.RootCert("estate", rk)
	require.NoError(t, err)
	d := troupe.DirFor(t.TempDir(), "estate")
	require.NoError(t, d.WriteRoot(root))
	l := troupe.NewLedger("estate", root)
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signerNode := b.NodeID()
	if !intermediate {
		signerNode = "node-s"
	}
	inter, err := troupe.IssueIntermediate(rk, root, "estate", signerNode, ik.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(t, err)
	selfJoin, err := troupe.SignJoin(ik, inter, "estate", signerNode, signerNode+".lan:7443", "user:owner", time.Now())
	require.NoError(t, err)
	_, err = l.Merge(selfJoin)
	require.NoError(t, err)
	if intermediate {
		require.NoError(t, d.WriteIntermediate(inter, ik))
	} else {
		_, nk, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		leaf, err := troupe.IssueNode(ik, inter, "estate", b.NodeID(), nk.Public(), time.Now(), troupe.NodeTTL)
		require.NoError(t, err)
		require.NoError(t, d.WriteNode(leaf, inter, nk))
		me, err := troupe.SignJoin(ik, inter, "estate", b.NodeID(), "me.lan:7443", "user:owner", time.Now())
		require.NoError(t, err)
		_, err = l.Merge(me)
		require.NoError(t, err)
	}
	a.Troupe = &troupe.Keeper{Dir: d, Troupe: "estate", Node: b.NodeID()}
	require.NoError(t, a.Troupe.Ensure())
	a.Peers = peer.NewLedger(b.NodeID(), l, &peer.Credentials{Certificate: a.Troupe.Current, Roots: a.Troupe.Roots})
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	return &signRig{t: t, a: a, rk: rk, root: root, call: map[string]func(context.Context, json.RawMessage) (any, error){
		rpc.MethodTroupeSign: hs.Map[rpc.MethodTroupeSign], rpc.MethodTroupeIdentity: hs.Map[rpc.MethodTroupeIdentity],
	}}
}

func as(id string) context.Context {
	return authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: id})
}

func (r *signRig) sign(ctx context.Context, node, door string) (rpc.TroupeSignResponse, ed25519.PublicKey, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(r.t, err)
	params, _ := json.Marshal(rpc.TroupeSignRequest{Troupe: "estate", Node: node, PublicKey: pub, Door: door})
	out, err := r.call[rpc.MethodTroupeSign](ctx, params)
	if err != nil {
		return rpc.TroupeSignResponse{}, pub, err
	}
	return out.(rpc.TroupeSignResponse), pub, nil
}

// A principal holding join on the troupe gets a seven-day leaf for the key
// it sent, the chain and root to check it with, and the entries, which now
// record its join through this node.
func TestTroupeSignAdmitsAPrincipalHoldingJoin(t *testing.T) {
	r := newSignRig(t, true)
	resp, pub, err := r.sign(as("member"), "node-b", "b.lan:7443")
	require.NoError(t, err)
	leaf, issuer, root, err := troupe.ParseSigned(resp.Chain, resp.Root)
	require.NoError(t, err)
	require.Equal(t, r.root.Raw, root.Raw)
	require.NoError(t, troupe.CheckLeaf(leaf, issuer, root, "estate", "node-b", pub, time.Now()))
	require.WithinDuration(t, time.Now().Add(troupe.NodeTTL), leaf.NotAfter, time.Minute)

	l := troupe.NewLedger("estate", root)
	es, err := troupe.Decode(resp.Entries)
	require.NoError(t, err)
	_, err = l.Merge(es...)
	require.NoError(t, err)
	b, ok := l.View().Members["node-b"]
	require.True(t, ok)
	require.Equal(t, "b.lan:7443", b.Door)
	require.Equal(t, r.a.Backend.NodeID(), b.AdmittedBy)
}

// Without join the answer is a refusal, whatever else the caller holds; a
// node never signs itself in, nor a node id that has left.
func TestTroupeSignRefusals(t *testing.T) {
	r := newSignRig(t, true)
	_, _, err := r.sign(as("reader"), "node-b", "b.lan:7443")
	require.ErrorContains(t, err, "join")
	_, _, err = r.sign(as("0"), r.a.Backend.NodeID(), "b.lan:7443")
	require.ErrorContains(t, err, "itself")
	for _, door := range []string{"", "b.lan", "0.0.0.0:7443", ":7443"} {
		_, _, err = r.sign(as("0"), "node-b", door)
		require.ErrorContains(t, err, "door", door)
	}
	params, _ := json.Marshal(rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: []byte{1, 2, 3}, Door: "b:1"})
	_, err = r.call[rpc.MethodTroupeSign](as("0"), params)
	require.ErrorContains(t, err, "public_key")

	revoke, err := troupe.SignRevoke(r.rk, "estate", "node-gone", time.Now())
	require.NoError(t, err)
	_, err = r.a.Peers.(*peer.Ledger).Merge(revoke)
	require.NoError(t, err)
	_, _, err = r.sign(as("0"), "node-gone", "g.lan:7443")
	require.ErrorContains(t, err, "does not return")
}

// A node holding only a leaf refuses to sign and names the members that can.
func TestALeafOnlyNodeRefusesToSignAndNamesWhoCan(t *testing.T) {
	r := newSignRig(t, false)
	_, _, err := r.sign(as("0"), "node-b", "b.lan:7443")
	require.ErrorContains(t, err, "cannot sign")
	require.ErrorContains(t, err, "node-s (node-s.lan:7443)")
}

// identity tells a would-be joiner who it is, which root it would trust,
// and which nodes can sign; it takes join, like sign.
func TestTroupeIdentity(t *testing.T) {
	r := newSignRig(t, true)
	params, _ := json.Marshal(rpc.TroupeIdentityRequest{Troupe: "estate"})
	_, err := r.call[rpc.MethodTroupeIdentity](as("reader"), params)
	require.ErrorContains(t, err, "join")
	out, err := r.call[rpc.MethodTroupeIdentity](as("member"), params)
	require.NoError(t, err)
	id := out.(rpc.TroupeIdentityResponse)
	require.Equal(t, "member", id.Principal.ID)
	require.Equal(t, r.root.Raw, id.Root)
	require.Len(t, id.RootSHA256, 64)
	require.Equal(t, []string{r.a.Backend.NodeID()}, id.Signers)

	params, _ = json.Marshal(rpc.TroupeIdentityRequest{Troupe: "other"})
	_, err = r.call[rpc.MethodTroupeIdentity](as("0"), params)
	require.ErrorContains(t, err, "not \"other\"")
}

// A principal holding join cannot mint a certificate for a node that is
// already a member: that would let it act as that node. The node itself
// comes back by signing the request with its old key, even an expired one.
func TestAMembersIdIsSignedAgainOnlyWithItsOldKey(t *testing.T) {
	r := newSignRig(t, true)
	oldPub, oldKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	first, err := r.call[rpc.MethodTroupeSign](as("member"), signParams(t, rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: oldPub, Door: "b.lan:7443"}))
	require.NoError(t, err)
	chain := first.(rpc.TroupeSignResponse).Chain

	newPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: newPub, Door: "b.lan:7443"}
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.ErrorContains(t, err, "already a member")

	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sig, err := troupe.SignRejoin(stranger, "estate", "node-b", newPub, "b.lan:7443")
	require.NoError(t, err)
	req.Rejoin = &rpc.TroupeRejoin{Chain: chain, Sig: sig}
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.ErrorContains(t, err, "old certificate's key")

	sig, err = troupe.SignRejoin(oldKey, "estate", "node-b", newPub, "b.lan:7443")
	require.NoError(t, err)
	req.Rejoin = &rpc.TroupeRejoin{Chain: chain, Sig: sig}
	req.Door = "b.tail:7443"
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.ErrorContains(t, err, "old certificate's key", "the proof covers the door")
	req.Door = "b.lan:7443"
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.NoError(t, err)
}

func signParams(t *testing.T, req rpc.TroupeSignRequest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	return raw
}

// The join troupe.sign records names the principal that asked as the
// node's owner; a renewal from a new door keeps it.
func TestTroupeSignRecordsTheCallerAsOwner(t *testing.T) {
	r := newSignRig(t, true)
	_, _, err := r.sign(as("member"), "node-b", "b.lan:7443")
	require.NoError(t, err)
	m := r.a.Peers.(*peer.Ledger)
	require.Equal(t, "user:member", m.View().Members["node-b"].Owner)
}
