package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/jkauthz"
)

// fakeMember is a node of troupe estate holding an intermediate, answering
// identity and sign the way the daemon does.
type fakeMember struct {
	t      *testing.T
	root   *x509.Certificate
	inter  *x509.Certificate
	ikey   ed25519.PrivateKey
	signed int
	last   rpc.TroupeSignRequest
	tamper func(*rpc.TroupeSignResponse)
}

func newFakeMember(t *testing.T) *fakeMember {
	t.Helper()
	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := troupe.RootCert("estate", rk)
	require.NoError(t, err)
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	inter, err := troupe.IssueIntermediate(rk, root, "estate", "node-a", ik.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(t, err)
	return &fakeMember{t: t, root: root, inter: inter, ikey: ik}
}

func (f *fakeMember) sum() string {
	s := sha256.Sum256(f.root.Raw)
	return hex.EncodeToString(s[:])
}

func (f *fakeMember) TroupeIdentity(_ context.Context, name string) (*rpc.TroupeIdentityResponse, error) {
	return &rpc.TroupeIdentityResponse{Troupe: name, Node: "node-a", Principal: rpc.Principal{Kind: rpc.PrincipalUser, ID: "jack@auth.test"},
		Root: f.root.Raw, RootSHA256: f.sum(), Signers: []string{"node-a"}}, nil
}

func (f *fakeMember) TroupeSign(_ context.Context, req rpc.TroupeSignRequest) (*rpc.TroupeSignResponse, error) {
	f.signed++
	f.last = req
	leaf, err := troupe.IssueNode(f.ikey, f.inter, req.Troupe, req.Node, ed25519.PublicKey(req.PublicKey), time.Now(), troupe.NodeTTL)
	if err != nil {
		return nil, err
	}
	self, err := troupe.SignJoin(f.ikey, f.inter, req.Troupe, "node-a", "a.lan:7443", "user:owner", time.Now())
	require.NoError(f.t, err)
	join, err := troupe.SignJoin(f.ikey, f.inter, req.Troupe, req.Node, req.Door, "user:owner", time.Now())
	require.NoError(f.t, err)
	resp := &rpc.TroupeSignResponse{Chain: [][]byte{leaf.Raw, f.inter.Raw}, Root: f.root.Raw, Entries: troupe.Encode([]troupe.Entry{self, join})}
	if f.tamper != nil {
		f.tamper(resp)
	}
	return resp, nil
}

type joinRig struct {
	site joinSite
	out  *bytes.Buffer
}

func newJoinRig(t *testing.T, cfg string) *joinRig {
	t.Helper()
	dir := t.TempDir()
	if cfg != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600))
	}
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	return &joinRig{site: joinSite{loaded: loaded, stateDir: t.TempDir(), node: "node-b"}, out: &bytes.Buffer{}}
}

func (r *joinRig) opts(o joinOpts) joinOpts {
	o.Out, o.UID = r.out, 1000
	if o.Delegate == "" && !o.Interactive {
		o.Delegate = delegateNone
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	return o
}

func (r *joinRig) config(t *testing.T) map[string]any {
	t.Helper()
	raw := map[string]any{}
	_, err := toml.DecodeFile(r.site.loaded.ConfigPath, &raw)
	require.NoError(t, err)
	return raw
}

// Joining writes this node's certificate from the key it made, the root,
// the entries it was handed, and the config a figaro-provider member
// needs, mapping the local uid to the principal that signed in.
func TestTroupeJoinWritesTheCertificateAndTheConfig(t *testing.T) {
	f := newFakeMember(t)
	r := newJoinRig(t, "default_outfit = \"mock\"\n")
	err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "b.lan:7443", Listen: "0.0.0.0:7443", Yes: true}))
	require.NoError(t, err, r.out.String())

	d := troupe.DirFor(r.site.stateDir, "estate")
	pair, err := d.Node()
	require.NoError(t, err)
	require.Equal(t, troupe.Identity("estate", "node-b"), pair.Leaf.URIs[0].String())
	root, err := d.Root()
	require.NoError(t, err)
	require.Equal(t, f.root.Raw, root.Raw)
	_, _, err = d.Intermediate()
	require.True(t, errors.Is(err, os.ErrNotExist), "a joined node holds no intermediate")
	joined, err := d.Joined()
	require.NoError(t, err)
	require.Len(t, joined, 2)

	cfg := r.config(t)
	require.Equal(t, "mock", cfg["default_outfit"], "the rest of the file survived")
	require.Equal(t, map[string]any{"name": "estate", "provider": "figaro"}, cfg["troupe"])
	require.Equal(t, map[string]any{"listen": "0.0.0.0:7443", "advertise": "b.lan:7443"}, cfg["door"].(map[string]any)["tls"])
	require.Equal(t, "user:jack@auth.test", cfg["authz"].(map[string]any)["peercred"].(map[string]any)["uids"].(map[string]any)["1000"])

	reloaded, err := config.Load(filepath.Dir(r.site.loaded.ConfigPath))
	require.NoError(t, err)
	p, err := reloaded.TroupeProvider()
	require.NoError(t, err)
	require.Equal(t, config.ProviderFigaro, p)
	require.Contains(t, r.out.String(), "admitted by node-a")
}

// Before anything is signed the wizard shows the root's sha256 and asks;
// a pin must match, a refusal signs nothing, and without a terminal it
// will not guess.
func TestTroupeJoinTrustsTheRootOnlyWhenTold(t *testing.T) {
	f := newFakeMember(t)
	cases := []struct {
		name string
		o    joinOpts
		ok   bool
	}{
		{"no terminal, no flag", joinOpts{}, false},
		{"answered no", joinOpts{Interactive: true, In: strings.NewReader("n\n")}, false},
		{"answered yes", joinOpts{Interactive: true, In: strings.NewReader("y\nnone\n")}, true},
		{"a wrong pin", joinOpts{RootSHA256: strings.Repeat("0", 64), Yes: true}, false},
		{"the right pin", joinOpts{RootSHA256: f.sum()}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := f.signed
			r := newJoinRig(t, "")
			c.o.Door = "b.lan:7443"
			err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(c.o))
			if c.ok {
				require.NoError(t, err)
				require.Equal(t, before+1, f.signed)
				return
			}
			require.Error(t, err)
			require.Equal(t, before, f.signed, "signed before the root was trusted")
			require.Contains(t, r.out.String()+err.Error(), f.sum())
			_, err = troupe.DirFor(r.site.stateDir, "estate").Root()
			require.Error(t, err, "wrote a root it did not trust")
		})
	}
}

// What the door hands back is checked before anything is written.
func TestTroupeJoinChecksWhatTheDoorReturns(t *testing.T) {
	other := newFakeMember(t)
	cases := map[string]func(*rpc.TroupeSignResponse){
		"another root":           func(r *rpc.TroupeSignResponse) { r.Root = other.root.Raw },
		"a leaf for another key": func(r *rpc.TroupeSignResponse) { r.Chain[0] = otherLeaf(t, other) },
		"no join recorded":       func(r *rpc.TroupeSignResponse) { r.Entries = r.Entries[:1] },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeMember(t)
			f.tamper = tamper
			r := newJoinRig(t, "")
			err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "b.lan:7443", Yes: true}))
			require.Error(t, err)
			_, err = troupe.DirFor(r.site.stateDir, "estate").Node()
			require.Error(t, err, "kept a certificate that failed its checks")
			_, err = os.Stat(r.site.loaded.ConfigPath)
			require.True(t, os.IsNotExist(err), "wrote config after a failed join")
		})
	}
}

func otherLeaf(t *testing.T, f *fakeMember) []byte {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	leaf, err := troupe.IssueNode(f.ikey, f.inter, "estate", "node-b", k.Public(), time.Now(), troupe.NodeTTL)
	require.NoError(t, err)
	return leaf.Raw
}

// A node already in a troupe, or configured with static peers or door
// certificate files, is refused before the door is asked anything.
func TestTroupeJoinRefusesAConfigItWouldContradict(t *testing.T) {
	for _, cfg := range []string{
		"[troupe]\nname = \"other\"\n",
		"[peers.a1]\nendpoint = \"tls://127.0.0.1:7444\"\nidentity = \"spiffe://x/node/a1\"\nca = \"c\"\ncert = \"c\"\nkey = \"k\"\n",
		"[door.tls]\nlisten = \"127.0.0.1:7443\"\ncert = \"c\"\nkey = \"k\"\nclient_ca = \"ca\"\n",
	} {
		f := newFakeMember(t)
		r := newJoinRig(t, cfg)
		err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "b.lan:7443", Yes: true}))
		require.Error(t, err, cfg)
		require.Zero(t, f.signed, cfg)
	}
	for _, door := range []string{"", "0.0.0.0:7443", "b.lan"} {
		f := newFakeMember(t)
		r := newJoinRig(t, "")
		err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: door, Yes: true}))
		require.ErrorContains(t, err, "door", door)
	}
}

// The door's own node is not joined through itself.
func TestTroupeJoinRefusesItsOwnDoor(t *testing.T) {
	f := newFakeMember(t)
	r := newJoinRig(t, "")
	r.site.node = "node-a"
	err := troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "a.lan:7443", Yes: true}))
	require.ErrorContains(t, err, "own")
}

// The uid already maps to user:<uid> by default, so only another
// principal is written.
func TestPeercredIsWrittenOnlyForAnotherPrincipal(t *testing.T) {
	require.Equal(t, "", peercredFor(1000, rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}))
	require.Equal(t, "", peercredFor(1000, rpc.Principal{Kind: rpc.PrincipalNode, ID: "node-a"}))
	require.Equal(t, "user:jack", peercredFor(1000, rpc.Principal{Kind: rpc.PrincipalUser, ID: "jack"}))
}

// Joining again from a node that still holds its old leaf proves it with
// the old key, which is what lets a member's id be signed a second time.
func TestTroupeJoinAgainCarriesProofOfTheOldKey(t *testing.T) {
	f := newFakeMember(t)
	r := newJoinRig(t, "")
	require.NoError(t, troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "b.lan:7443", Yes: true})))
	require.Nil(t, f.last.Rejoin)
	old, err := troupe.DirFor(r.site.stateDir, "estate").Node()
	require.NoError(t, err)

	reloaded, err := config.Load(filepath.Dir(r.site.loaded.ConfigPath))
	require.NoError(t, err)
	r.site.loaded = reloaded
	require.NoError(t, troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{})))
	require.NotNil(t, f.last.Rejoin)
	leaf, issuer, root, err := troupe.ParseSigned(f.last.Rejoin.Chain, f.root.Raw)
	require.NoError(t, err)
	require.Equal(t, old.Leaf.Raw, leaf.Raw)
	require.NoError(t, troupe.CheckRejoin(leaf, issuer, root, f.last.Rejoin.Sig, "estate", "node-b", ed25519.PublicKey(f.last.PublicKey), "b.lan:7443"))
}

// What a node lends the troupe policy is chosen, never assumed: a run
// without a terminal must say, and an unclear answer signs nothing.
func TestTroupeJoinNeedsADelegationChoice(t *testing.T) {
	f := newFakeMember(t)
	for name, o := range map[string]joinOpts{
		"no terminal, no flag":        {Yes: true},
		"a word that is not a choice": {Yes: true, Delegate: "some"},
		"asked, answered neither":     {Yes: true, Interactive: true, In: strings.NewReader("maybe\n")},
	} {
		r := newJoinRig(t, "")
		o.Door = "b.lan:7443"
		before := f.signed
		opts := r.opts(o)
		if name == "no terminal, no flag" {
			opts.Delegate = ""
		}
		err := troupeJoin(context.Background(), f, r.site, "estate", opts)
		require.Error(t, err, name)
		require.Equal(t, before, f.signed, "%s: signed without a choice", name)
	}
}

// --delegate all binds the troupe to an all-granting role in the node's
// policy, starting one from the default when there is none, and joining
// again does not bind it twice; none writes nothing.
func TestTroupeJoinWritesTheDelegation(t *testing.T) {
	f := newFakeMember(t)
	r := newJoinRig(t, "default_outfit = \"mock\"\n")
	require.NoError(t, troupeJoin(context.Background(), f, r.site, "estate", r.opts(joinOpts{Door: "b.lan:7443", Yes: true, Delegate: delegateAll})), r.out.String())
	require.Equal(t, "policy.toml", r.config(t)["authz"].(map[string]any)["policy"])
	path := filepath.Join(r.site.loaded.ConfigDir, "policy.toml")
	reloaded, err := config.Load(r.site.loaded.ConfigDir)
	require.NoError(t, err)
	require.Equal(t, path, reloaded.PolicyPath())

	p := authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	require.NoError(t, p.Reload(path))
	lent := jkauthz.Subject{Principal: authz.TroupePrincipal("estate")}
	require.True(t, p.Allow(lent, rpc.VerbSend, authz.Aria("aaaa0001")).Allowed)
	self := jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "jack@auth.test"}}
	require.True(t, p.Allow(self, rpc.VerbKill, authz.Aria("aaaa0001")).Allowed, "the node's own principal lost its owner role")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	r.site.loaded = reloaded
	require.NoError(t, writeDelegation(r.out, reloaded, "estate", delegateAll, "user:1000"))
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(body), string(again), "a second join bound the troupe twice")

	none := newJoinRig(t, "default_outfit = \"mock\"\n")
	require.NoError(t, troupeJoin(context.Background(), f, none.site, "estate", none.opts(joinOpts{Door: "b.lan:7443", Yes: true, Delegate: delegateNone})))
	_, err = os.Stat(filepath.Join(none.site.loaded.ConfigDir, "policy.toml"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Contains(t, none.out.String(), "lends troupe estate nothing")
}
