package angelus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
permissions = ["*:*"]
[[bind]]
role = "owner"
principals = ["user:0"]

[roles.member]
permissions = ["join:troupe:estate"]
[[bind]]
role = "member"
principals = ["user:member"]

[roles.reader]
permissions = ["read:*"]
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

func (r *signRig) sign(ctx context.Context, node, endpoint string) (rpc.TroupeSignResponse, ed25519.PublicKey, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(r.t, err)
	params, _ := json.Marshal(rpc.TroupeSignRequest{Troupe: "estate", Node: node, PublicKey: pub, Endpoint: endpoint})
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
	require.Equal(t, "b.lan:7443", b.Endpoint)
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
	for _, endpoint := range []string{"", "b.lan", "0.0.0.0:7443", ":7443"} {
		_, _, err = r.sign(as("0"), "node-b", endpoint)
		require.ErrorContains(t, err, "door", endpoint)
	}
	params, _ := json.Marshal(rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: []byte{1, 2, 3}, Endpoint: "b:1"})
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
	first, err := r.call[rpc.MethodTroupeSign](as("member"), signParams(t, rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: oldPub, Endpoint: "b.lan:7443"}))
	require.NoError(t, err)
	chain := first.(rpc.TroupeSignResponse).Chain

	newPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: newPub, Endpoint: "b.lan:7443"}
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
	req.Endpoint = "b.tail:7443"
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.ErrorContains(t, err, "old certificate's key", "the proof covers the door")
	req.Endpoint = "b.lan:7443"
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
// node's owner; a renewal from a new endpoint keeps it.
func TestTroupeSignRecordsTheCallerAsOwner(t *testing.T) {
	r := newSignRig(t, true)
	_, _, err := r.sign(as("member"), "node-b", "b.lan:7443")
	require.NoError(t, err)
	m := r.a.Peers.(*peer.Ledger)
	require.Equal(t, "user:member", m.View().Members["node-b"].Owner)
}

// The attack through rejoin: q's intermediate mints a leaf for member b and
// offers it as b's old certificate. This node issued b's leaf itself and is
// not q, and q is not b's renewer, so the proof is refused.
func TestARejoinProofFromAnotherIntermediateIsRefused(t *testing.T) {
	r := newSignRig(t, true)
	_, _, err := r.sign(as("member"), "node-b", "b.lan:7443")
	require.NoError(t, err)

	_, qk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	qinter, err := troupe.IssueIntermediate(r.rk, r.root, "estate", "node-q", qk.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(t, err)
	forgedPub, forgedKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	leaf, err := troupe.IssueNode(qk, qinter, "estate", "node-b", forgedPub, time.Now(), troupe.NodeTTL)
	require.NoError(t, err)

	newPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sig, err := troupe.SignRejoin(forgedKey, "estate", "node-b", newPub, "evil.lan:7443")
	require.NoError(t, err)
	req := rpc.TroupeSignRequest{Troupe: "estate", Node: "node-b", PublicKey: newPub, Endpoint: "evil.lan:7443",
		Rejoin: &rpc.TroupeRejoin{Chain: [][]byte{leaf.Raw, qinter.Raw}, Sig: sig}}
	_, err = r.call[rpc.MethodTroupeSign](as("member"), signParams(t, req))
	require.ErrorContains(t, err, "came from node-q, which is neither")
}

func (r *signRig) signFor(ttlMillis int64) (*x509.Certificate, error) {
	leaf, _, err := r.signForNode("node-b", ttlMillis)
	return leaf, err
}

func (r *signRig) signForNode(node string, ttlMillis int64) (*x509.Certificate, time.Duration, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(r.t, err)
	params, _ := json.Marshal(rpc.TroupeSignRequest{Troupe: "estate", Node: node, PublicKey: pub, Endpoint: node + ".lan:7443", TTLMillis: ttlMillis})
	out, err := r.call[rpc.MethodTroupeSign](as("member"), params)
	if err != nil {
		return nil, 0, err
	}
	resp := out.(rpc.TroupeSignResponse)
	leaf, err := x509.ParseCertificate(resp.Chain[0])
	require.NoError(r.t, err)
	return leaf, time.Duration(resp.TTLMillis) * time.Millisecond, nil
}

// The issuer grants the life a node asks for up to its own max_leaf_ttl,
// never more, and seven days when it asks for nothing.
func TestTroupeSignGrantsTheAskedLifeUpToItsMost(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name  string
		asked time.Duration
		most  time.Duration
		want  time.Duration
	}{
		{"nothing asked", 0, 0, troupe.NodeTTL},
		{"30d under the default most", 30 * day, 0, 30 * day},
		{"90d capped at the default most", 90 * day, 0, troupe.MaxNodeTTL},
		{"30d capped at this issuer's 10d", 30 * day, 10 * day, 10 * day},
	}
	for _, c := range cases {
		r := newSignRig(t, true)
		r.a.Troupe.MaxTTL = c.most
		before := time.Now()
		leaf, err := r.signFor(c.asked.Milliseconds())
		require.NoError(t, err, c.name)
		require.WithinDuration(t, before.Add(c.want), leaf.NotAfter, time.Minute, c.name)
	}

	r := newSignRig(t, true)
	leaf, err := r.signFor(1 << 62)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(troupe.MaxNodeTTL), leaf.NotAfter, time.Minute, "ttl_ms past what a Duration holds")

	_, err = r.signFor(-1)
	require.ErrorContains(t, err, "ttl_ms -1")

	leaf, _, err = r.signForNode("node-f", 1)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(troupe.MinNodeTTL), leaf.NotAfter, time.Minute, "a 1 ms ask is floored at a day")
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// Asking past the issuer's most is granted the most, not refused, so a
// node never falls out over a mismatch at renewal. The issuer says so once
// a day per node, and the response carries what was granted.
func TestAnAskPastTheMostIsGrantedTheMostAndWarnedOncePerNode(t *testing.T) {
	const day = 24 * time.Hour
	warned := captureWarnings(t)
	r := newSignRig(t, true)
	_, granted, err := r.signForNode("node-b", (60 * day).Milliseconds())
	require.NoError(t, err)
	require.InDelta(t, float64(troupe.MaxNodeTTL), float64(granted), float64(time.Minute))
	_, _, err = r.signForNode("node-b", (60 * day).Milliseconds())
	require.Error(t, err, "node-b is a member now, so a second sign needs its old key")
	_, _, err = r.signForNode("node-c", (90 * day).Milliseconds())
	require.NoError(t, err)
	_, granted, err = r.signForNode("node-d", (14 * day).Milliseconds())
	require.NoError(t, err)
	require.InDelta(t, float64(14*day), float64(granted), float64(time.Minute))

	logged := warned.String()
	require.Contains(t, logged, "node=node-b asked_ms=5184000000 granted_ms=2592000000")
	require.Contains(t, logged, "node=node-c asked_ms=7776000000 granted_ms=2592000000")
	require.NotContains(t, logged, "node=node-d", "an ask under the most was warned about")

	h := &handlers{}
	now := time.Now()
	for range 3 {
		h.warnCapped("node-e", 60*day, 30*day, now)
	}
	h.warnCapped("node-e", 60*day, 30*day, now.Add(25*time.Hour))
	require.Equal(t, 2, strings.Count(warned.String(), "node=node-e"), "the warning is once per node per day")
}
