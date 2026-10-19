package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

type fakeKeyDoor struct {
	minted rpc.TroupeAPIKeyMintRequest
	list   rpc.TroupeAPIKeyListResponse
}

func (f *fakeKeyDoor) TroupeAPIKeyMint(_ context.Context, req rpc.TroupeAPIKeyMintRequest) (*rpc.TroupeAPIKeyMintResponse, error) {
	f.minted = req
	return &rpc.TroupeAPIKeyMintResponse{Key: rpc.TroupeAPIKey{ID: req.ID, Principal: req.Principal, By: "user:jack@auth.kelliher.info", Signer: "node-a", NotAfter: time.Now().Add(time.Duration(req.TTLMillis) * time.Millisecond).UnixMilli()}, Reached: []string{"node-b"}}, nil
}

func (f *fakeKeyDoor) TroupeAPIKeyRevoke(_ context.Context, troupe, id string) (*rpc.TroupeAPIKeyRevokeResponse, error) {
	return &rpc.TroupeAPIKeyRevokeResponse{Key: rpc.TroupeAPIKey{ID: id, Principal: "service:ci@troupe:estate", By: "user:0", RevokedBy: "user:0", RevokedVia: "node-a", RevokedAt: 1}}, nil
}

func (f *fakeKeyDoor) TroupeAPIKeyList(context.Context, string) (*rpc.TroupeAPIKeyListResponse, error) {
	return &f.list, nil
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"": apikey.DefaultTTL, "30d": 30 * 24 * time.Hour, "12h": 12 * time.Hour} {
		got, err := parseTTL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"0d", "-1d", "xd", "soon", "-5m"} {
		_, err := parseTTL(in)
		require.Error(t, err, in)
	}
}

func TestAKeyPrincipalTakesItsTroupeScope(t *testing.T) {
	p, err := keyPrincipal("estate", "service:ci")
	require.NoError(t, err)
	require.Equal(t, "service:ci@troupe:estate", p)
	p, err = keyPrincipal("estate", "user:alice@troupe:estate")
	require.NoError(t, err)
	require.Equal(t, "user:alice@troupe:estate", p)
	_, err = keyPrincipal("estate", "user:jack@auth.kelliher.info")
	require.Error(t, err)
	_, err = keyPrincipal("estate", "node:x")
	require.Error(t, err)
}

// The key goes to stdout alone, once; the node is sent its hash and never
// the key, and what it reports goes to stderr.
func TestMintPrintsTheKeyOnceAndSendsOnlyItsHash(t *testing.T) {
	d := &fakeKeyDoor{}
	var out, info bytes.Buffer
	require.NoError(t, apiKeyMint(context.Background(), d, &out, &info, "estate", "service:ci", apiKeyOpts{TTL: "30d"}, false))
	wire := strings.TrimSpace(out.String())
	k, err := apikey.Parse(wire)
	require.NoError(t, err)
	require.Equal(t, k.ID, d.minted.ID)
	require.Equal(t, k.Hash[:], d.minted.Hash)
	require.Equal(t, "service:ci@troupe:estate", d.minted.Principal)
	require.Equal(t, (30 * 24 * time.Hour).Milliseconds(), d.minted.TTLMillis)
	require.NotContains(t, info.String(), wire)
	require.Contains(t, info.String(), "minted by user:jack@auth.kelliher.info")
	require.Contains(t, info.String(), "node-b")

	require.Error(t, apiKeyMint(context.Background(), d, &out, &info, "estate", "service:ci", apiKeyOpts{TTL: "400d"}, false))
}

// A static key's stanza is what a node's config takes: decoded and loaded,
// it admits the key it was printed with.
func TestAStaticKeyStanzaAdmitsItsKey(t *testing.T) {
	var out, info bytes.Buffer
	require.NoError(t, apiKeyMint(context.Background(), nil, &out, &info, "estate", "service:ci", apiKeyOpts{TTL: "1d", Static: true}, false))
	wire := strings.TrimSpace(out.String())
	require.NotContains(t, info.String(), wire)
	stanza := info.String()[strings.Index(info.String(), "[[authz.api_keys]]"):]
	var cfg struct {
		Authz config.AuthzConfig `toml:"authz"`
	}
	_, err := toml.Decode(stanza, &cfg)
	require.NoError(t, err)
	keys, err := authz.NewAPIKeys("estate", cfg.Authz.APIKeys, nil)
	require.NoError(t, err)
	ctx, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	s, err := authz.SubjectOf(ctx)
	require.NoError(t, err)
	require.Equal(t, "service:ci@troupe:estate", s.Principal.String())
}

func TestListShowsWhoMintedEachKeyAndItsState(t *testing.T) {
	now := time.Now()
	d := &fakeKeyDoor{list: rpc.TroupeAPIKeyListResponse{Troupe: "estate", Caller: "user:0", All: true, Keys: []rpc.TroupeAPIKey{
		{ID: "aaaaaaaaaa", Principal: "service:ci@troupe:estate", By: "user:jack@auth.kelliher.info", Signer: "node-a", MintedAt: now.UnixMilli(), NotAfter: now.Add(time.Hour).UnixMilli()},
		{ID: "bbbbbbbbbb", Principal: "service:cd@troupe:estate", By: "user:0", Signer: "node-a", MintedAt: now.UnixMilli(), NotAfter: now.Add(time.Hour).UnixMilli(), RevokedBy: "user:0", RevokedAt: now.UnixMilli()},
		{ID: "cccccccccc", Principal: "user:alice@troupe:estate", By: "user:0", Signer: "node-a", MintedAt: now.Add(-2 * time.Hour).UnixMilli(), NotAfter: now.Add(-time.Hour).UnixMilli()},
	}}}
	var out bytes.Buffer
	require.NoError(t, apiKeyList(context.Background(), d, &out, "estate", false))
	s := out.String()
	require.Contains(t, s, "MINTED BY")
	require.Contains(t, s, "user:jack@auth.kelliher.info")
	require.Contains(t, s, "live")
	require.Contains(t, s, "revoked by user:0")
	require.Contains(t, s, "expired")
	require.Contains(t, s, "every key")
}

func TestLoginReadsOneKeyLineFromAPipe(t *testing.T) {
	wire, _, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString(wire + "\nnext line\n")
	w.Close()
	got, err := readAPIKey(r, &bytes.Buffer{}, "estate")
	require.NoError(t, err)
	require.Equal(t, wire, got)

	r, w, err = os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("eyJhbGciOiJSUzI1NiJ9.e30.x\n")
	w.Close()
	_, err = readAPIKey(r, &bytes.Buffer{}, "estate")
	require.ErrorContains(t, err, "not a figaro api key")
}
