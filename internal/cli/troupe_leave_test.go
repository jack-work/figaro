package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/troupe"
)

type fakeLeave struct{ asked int }

func (f *fakeLeave) TroupeLeave(_ context.Context, name string) (*rpc.TroupeLeaveResponse, error) {
	f.asked++
	return &rpc.TroupeLeaveResponse{Node: "node-b", Reached: []string{"node-a"}, Unreachable: []rpc.NodeUnreachable{{Node: "node-c", Error: "connection refused"}}}, nil
}

const memberConfig = "default_outfit = \"mock\"\n[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n[door.tls]\nlisten = \"10.0.0.2:7443\"\n"

// Leaving asks first, then the daemon signs and spreads it, and only then
// do the keys and the troupe config go; the rest of the config stays.
func TestTroupeLeaveAsksThenRemovesTheKeysAndConfig(t *testing.T) {
	r := newJoinRig(t, memberConfig)
	d := troupe.DirFor(r.site.stateDir, "estate")
	require.NoError(t, os.MkdirAll(string(d), 0o700))
	f := &fakeLeave{}

	err := troupeLeave(context.Background(), f, r.site, "estate", r.opts(joinOpts{Interactive: true, In: strings.NewReader("n\n")}))
	require.Error(t, err)
	require.Zero(t, f.asked)
	err = troupeLeave(context.Background(), f, r.site, "estate", r.opts(joinOpts{}))
	require.ErrorContains(t, err, "--yes")
	require.Zero(t, f.asked)

	require.NoError(t, troupeLeave(context.Background(), f, r.site, "estate", r.opts(joinOpts{Yes: true})))
	require.Equal(t, 1, f.asked)
	_, err = os.Stat(string(d))
	require.True(t, os.IsNotExist(err), "the troupe keys survived a leave")
	cfg := r.config(t)
	require.Nil(t, cfg["troupe"])
	require.Nil(t, cfg["door"])
	require.Equal(t, "mock", cfg["default_outfit"])
	require.Regexp(t, `node-a\s+reached`, r.out.String())
	require.Regexp(t, `node-c\s+not reached: connection refused`, r.out.String())
}

func TestTroupeLeaveRefusesAnotherTroupe(t *testing.T) {
	r := newJoinRig(t, memberConfig)
	f := &fakeLeave{}
	require.ErrorContains(t, troupeLeave(context.Background(), f, r.site, "other", r.opts(joinOpts{Yes: true})), "not in troupe other")
	require.Zero(t, f.asked)
}

type fakeEntries struct{ got []troupe.Entry }

func (f *fakeEntries) TroupeEntries(_ context.Context, req rpc.TroupeEntriesRequest) (*rpc.TroupeEntriesResponse, error) {
	es, err := troupe.Decode(req.Entries)
	f.got = es
	return &rpc.TroupeEntriesResponse{Reached: []string{"node-a"}}, err
}

func rootKeyPEM(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), k
}

// A revoke is signed with the root read from stdin, only if it is this
// troupe's root, and goes to the daemon to spread.
func TestTroupeRevokeSignsWithTheTroupesRootOnly(t *testing.T) {
	r := newJoinRig(t, memberConfig)
	raw, k := rootKeyPEM(t)
	root, err := troupe.RootCert("estate", k)
	require.NoError(t, err)
	require.NoError(t, troupe.DirFor(r.site.stateDir, "estate").WriteRoot(root))

	f := &fakeEntries{}
	wrong, _ := rootKeyPEM(t)
	require.ErrorContains(t, troupeRevoke(context.Background(), f, bytes.NewReader(wrong), r.out, r.site, "estate", "node-c"), "not troupe estate's root")
	require.Empty(t, f.got)

	require.NoError(t, troupeRevoke(context.Background(), f, bytes.NewReader(raw), r.out, r.site, "estate", "node-c"))
	require.Len(t, f.got, 1)
	require.Equal(t, troupe.Revoke, f.got[0].Kind)
	require.Equal(t, "node-c", f.got[0].Node)
	l := troupe.NewLedger("estate", root)
	_, err = l.Merge(f.got...)
	require.NoError(t, err)
	require.Contains(t, r.out.String(), "node-a")
}

// troupe renewer signs, with this troupe's root only, a statement that
// lets another node's intermediate issue a node's certificates.
func TestTroupeRenewerSignsWithTheTroupesRootOnly(t *testing.T) {
	r := newJoinRig(t, memberConfig)
	raw, k := rootKeyPEM(t)
	root, err := troupe.RootCert("estate", k)
	require.NoError(t, err)
	require.NoError(t, troupe.DirFor(r.site.stateDir, "estate").WriteRoot(root))

	f := &fakeEntries{}
	wrong, _ := rootKeyPEM(t)
	require.ErrorContains(t, troupeRenewer(context.Background(), f, bytes.NewReader(wrong), r.out, r.site, "estate", "node-c", "node-a"), "not troupe estate's root")
	require.Empty(t, f.got)

	require.NoError(t, troupeRenewer(context.Background(), f, bytes.NewReader(raw), r.out, r.site, "estate", "node-c", "node-a"))
	require.Len(t, f.got, 1)
	l := troupe.NewLedger("estate", root)
	_, err = l.Merge(f.got...)
	require.NoError(t, err)
	require.True(t, l.View().MayIssue("node-c", "node-a"))
}

// Under the figaro provider troupe ls says who owns each node, who
// admitted it, which can sign, and which nodes are out and how.
func TestTroupeLsShowsAdmissionsSignersAndWhoIsOut(t *testing.T) {
	var buf strings.Builder
	writeTroupeMembers(&buf, &rpc.TroupeMembersResponse{
		Node: "node-b", Provider: "figaro", AdmittedBy: "node-a", Owner: "user:guest", Version: 9,
		Peers: []rpc.TroupeMember{
			{Node: "node-a", Endpoint: "tls://a.lan:7443", Owner: "user:jack", AdmittedBy: "node-a", Signs: true, Met: true, Version: 9, SeenAt: time.Now().UnixMilli()},
			{Node: "node-c", Endpoint: "tls://c.lan:7443", Owner: "user:jack", AdmittedBy: "node-a"},
		},
		Gone: []rpc.TroupeGone{{Node: "node-d", By: "leave"}, {Node: "node-e", By: "revoke"}},
		Keys: &rpc.TroupeKeys{Troupe: "estate", NodeExpires: 1},
	})
	out := buf.String()
	require.Contains(t, out, "admitted by node-a, owned by user:guest")
	require.Contains(t, out, "no intermediate, so a signer renews it")
	require.Regexp(t, `node-a\s+tls://a\.lan:7443\s+user:jack\s+node-a\s+yes\s+just now\s+9`, out)
	require.Regexp(t, `node-c\s+tls://c\.lan:7443\s+user:jack\s+node-a\s+-\s+-\s+not met`, out)
	require.Contains(t, out, "out: node-d (leave)")
	require.Contains(t, out, "out: node-e (revoke)")
}
