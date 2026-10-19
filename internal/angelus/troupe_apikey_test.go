package angelus

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/jkauthz"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

type keyRig struct {
	*signRig
	hs *Handlers
}

func newKeyRig(t *testing.T, intermediate bool) *keyRig {
	r := newSignRig(t, intermediate)
	return &keyRig{signRig: r, hs: NewHandlers(ServerConfig{Angelus: r.a, Config: &config.Loaded{}, Ctx: context.Background()})}
}

func (r *keyRig) do(ctx context.Context, method string, req any) (any, error) {
	params, _ := json.Marshal(req)
	return r.hs.Map[method](ctx, params)
}

func (r *keyRig) mint(ctx context.Context, principal string, ttl time.Duration, nodes ...string) (rpc.TroupeAPIKeyMintResponse, apikey.Key, error) {
	_, k, err := apikey.New(rand.Reader)
	require.NoError(r.t, err)
	out, err := r.do(ctx, rpc.MethodTroupeAPIKeyMint, rpc.TroupeAPIKeyMintRequest{Troupe: "estate", ID: k.ID, Principal: principal, Hash: k.Hash[:], TTLMillis: ttl.Milliseconds(), Nodes: nodes})
	if err != nil {
		return rpc.TroupeAPIKeyMintResponse{}, k, err
	}
	return out.(rpc.TroupeAPIKeyMintResponse), k, nil
}

func (r *keyRig) list(ctx context.Context) rpc.TroupeAPIKeyListResponse {
	out, err := r.do(ctx, rpc.MethodTroupeAPIKeyList, rpc.TroupeAPIKeyListRequest{Troupe: "estate"})
	require.NoError(r.t, err)
	return out.(rpc.TroupeAPIKeyListResponse)
}

func asKey(principal string) context.Context {
	p, err := jkauthz.ParsePrincipal(principal)
	if err != nil {
		panic(err)
	}
	return authz.WithSubject(context.Background(), jkauthz.Subject{Principal: p})
}

// Minting takes admin on the troupe; the entry records who asked and which
// node signed, and only the hash ever reaches the node.
func TestMintingAKeyTakesAdminAndRecordsTheMinter(t *testing.T) {
	r := newKeyRig(t, true)
	out, k, err := r.mint(as("0"), "service:ci@troupe:estate", 0)
	require.NoError(t, err)
	require.Equal(t, k.ID, out.Key.ID)
	require.Equal(t, "user:0", out.Key.By)
	require.Equal(t, r.a.Backend.NodeID(), out.Key.Signer)
	require.Equal(t, []string{r.a.Backend.NodeID()}, out.Key.Nodes, "a key is good on the minting node alone unless it names others")
	require.InDelta(t, time.Now().Add(apikey.DefaultTTL).UnixMilli(), out.Key.NotAfter, float64(time.Minute.Milliseconds()))
	rec := r.a.Peers.(*peer.Ledger).APIKeys()[k.ID]
	require.True(t, k.Matches(rec.Hash))

	_, _, err = r.mint(as("member"), "service:ci@troupe:estate", 0)
	require.ErrorContains(t, err, "admin")
	for name, p := range map[string]string{"another troupe": "service:ci@troupe:other", "an issuer's user": "user:jack@auth.kelliher.info", "a sandbox": "sandbox:ci@troupe:estate"} {
		_, _, err := r.mint(as("0"), p, 0)
		require.Error(t, err, name)
	}
	_, _, err = r.mint(as("0"), "service:ci@troupe:estate", apikey.MaxTTL+time.Hour)
	require.ErrorContains(t, err, "lives")
	_, err = r.do(as("0"), rpc.MethodTroupeAPIKeyMint, rpc.TroupeAPIKeyMintRequest{Troupe: "estate", ID: k.ID, Principal: "service:cd@troupe:estate", Hash: k.Hash[:]})
	require.ErrorContains(t, err, "taken")
	_, err = r.do(as("0"), rpc.MethodTroupeAPIKeyMint, rpc.TroupeAPIKeyMintRequest{Troupe: "estate", ID: "aaaaaaaaaa", Principal: "service:cd@troupe:estate", Hash: k.Hash[:5]})
	require.ErrorContains(t, err, "hash")
}

func TestALeafOnlyNodeCannotMintAKey(t *testing.T) {
	r := newKeyRig(t, false)
	_, _, err := r.mint(as("0"), "service:ci@troupe:estate", 0)
	require.ErrorContains(t, err, "cannot sign")
	require.ErrorContains(t, err, "node-s")
}

// The key's own principal, its minter, or a troupe admin may revoke it; no
// one else may, and a second revoke changes nothing.
func TestWhoMayRevokeAKey(t *testing.T) {
	r := newKeyRig(t, true)
	_, k, err := r.mint(as("0"), "service:ci@troupe:estate", time.Hour)
	require.NoError(t, err)
	revoke := func(ctx context.Context) (rpc.TroupeAPIKeyRevokeResponse, error) {
		out, err := r.do(ctx, rpc.MethodTroupeAPIKeyRevoke, rpc.TroupeAPIKeyRevokeRequest{Troupe: "estate", ID: k.ID})
		if err != nil {
			return rpc.TroupeAPIKeyRevokeResponse{}, err
		}
		return out.(rpc.TroupeAPIKeyRevokeResponse), nil
	}
	_, err = revoke(as("reader"))
	require.ErrorContains(t, err, "minter")
	_, err = revoke(asKey("service:cd@troupe:estate"))
	require.Error(t, err)

	out, err := revoke(asKey("service:ci@troupe:estate"))
	require.NoError(t, err)
	require.False(t, out.Already)
	require.Equal(t, "service:ci@troupe:estate", out.Key.RevokedBy)
	require.Equal(t, r.a.Backend.NodeID(), out.Key.RevokedVia)
	require.NotNil(t, r.a.Peers.(*peer.Ledger).APIKeys()[k.ID].Revoked)

	out, err = revoke(as("0"))
	require.NoError(t, err)
	require.True(t, out.Already)

	_, err = r.do(as("0"), rpc.MethodTroupeAPIKeyRevoke, rpc.TroupeAPIKeyRevokeRequest{Troupe: "estate", ID: "aaaaaaaaaa"})
	require.ErrorContains(t, err, "no api key")
}

// A troupe admin lists every key with its minter; anyone else sees only the
// keys they minted or that name them, and learns who they are.
func TestListingKeysShowsWhoMintedEach(t *testing.T) {
	r := newKeyRig(t, true)
	_, k1, err := r.mint(as("0"), "service:ci@troupe:estate", time.Hour)
	require.NoError(t, err)
	_, _, err = r.mint(as("0"), "user:alice@troupe:estate", time.Hour)
	require.NoError(t, err)

	all := r.list(as("0"))
	require.True(t, all.All)
	require.Len(t, all.Keys, 2)
	for _, k := range all.Keys {
		require.Equal(t, "user:0", k.By)
	}

	mine := r.list(asKey("service:ci@troupe:estate"))
	require.False(t, mine.All)
	require.Equal(t, "service:ci@troupe:estate", mine.Caller)
	require.Len(t, mine.Keys, 1)
	require.Equal(t, k1.ID, mine.Keys[0].ID)

	require.Empty(t, r.list(as("reader")).Keys)
}

func TestAStaticTroupeMintsNoKeysButSaysWhoYouAre(t *testing.T) {
	r := newKeyRig(t, true)
	r.a.Peers, _ = peer.NewStatic(0, nil, nil)
	r.hs = NewHandlers(ServerConfig{Angelus: r.a, Config: &config.Loaded{}, Ctx: context.Background()})
	_, _, err := r.mint(as("0"), "service:ci@troupe:estate", 0)
	require.ErrorContains(t, err, "static")
	out := r.list(asKey("service:ci@troupe:estate"))
	require.Equal(t, "service:ci@troupe:estate", out.Caller)
	require.Empty(t, out.Keys)
}

func (r *keyRig) member(node string) {
	r.t.Helper()
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(r.t, err)
	inter, err := troupe.IssueIntermediate(r.rk, r.root, "estate", node, ik.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(r.t, err)
	e, err := troupe.SignJoin(ik, inter, "estate", node, node+".lan:7443", "user:owner", time.Now())
	require.NoError(r.t, err)
	_, err = r.a.Peers.(*peer.Ledger).Merge(e)
	require.NoError(r.t, err)
}

// --nodes names members by id or unique prefix, or all of them; an unknown
// node, or all beside others, is refused, and ls shows the audience.
func TestAKeyNamesTheNodesItIsGoodOn(t *testing.T) {
	r := newKeyRig(t, true)
	r.member("node-bbbbbbbb-1")
	r.member("node-bbbbbbbb-2")
	self := r.a.Backend.NodeID()

	out, _, err := r.mint(as("0"), "service:ci@troupe:estate", time.Hour, "node-bbbbbbbb-2", self)
	require.NoError(t, err)
	want := []string{"node-bbbbbbbb-2", self}
	slices.Sort(want)
	require.Equal(t, want, out.Key.Nodes)

	out, _, err = r.mint(as("0"), "service:ci@troupe:estate", time.Hour, apikey.AllNodes)
	require.NoError(t, err)
	require.Equal(t, []string{apikey.AllNodes}, out.Key.Nodes)

	for name, nodes := range map[string][]string{
		"an unknown node":     {"node-zzzzzzzz"},
		"an ambiguous prefix": {"node-bbbbbbbb"},
		"all beside one":      {apikey.AllNodes, self},
	} {
		_, _, err := r.mint(as("0"), "service:ci@troupe:estate", time.Hour, nodes...)
		require.Error(t, err, name)
	}

	for _, k := range r.list(as("0")).Keys {
		require.NotEmpty(t, k.Nodes)
	}
}
