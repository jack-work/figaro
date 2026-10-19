package angelus

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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

const troupeAdminPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[[bind]]
role = "owner"
principals = ["user:0"]

[roles.member]
grants = [{ verbs = ["join", "read"], on = ["troupe:estate"] }]
[[bind]]
role = "member"
principals = ["user:member"]
`

// Over a unix or web door, pushing entries takes admin on the troupe: a
// principal allowed to join is not thereby allowed to push revokes. A
// static troupe has no entries to trade.
func TestTroupeEntriesFromACallerNeedAdminOnTheTroupe(t *testing.T) {
	b := store.NewTestBackend(t)
	a := &Angelus{Registry: NewRegistry(), Backend: b}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "0"}, StoreFacts(b))
	path := filepath.Join(t.TempDir(), "policy.toml")
	require.NoError(t, os.WriteFile(path, []byte(troupeAdminPolicy), 0o600))
	require.NoError(t, a.Policies.Reload(path))

	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	call := hs.Map[rpc.MethodTroupeEntries]
	params, _ := json.Marshal(rpc.TroupeEntriesRequest{Troupe: "estate"})
	_, err := call(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"}), params)
	require.ErrorContains(t, err, "static")

	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := troupe.RootCert("estate", rk)
	require.NoError(t, err)
	a.Peers = peer.NewLedger(b.NodeID(), troupe.NewLedger("estate", root), &peer.Credentials{})
	hs = NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	call = hs.Map[rpc.MethodTroupeEntries]
	revoke, err := troupe.SignRevoke(rk, "estate", "node-x", time.Now())
	require.NoError(t, err)
	params, _ = json.Marshal(rpc.TroupeEntriesRequest{Troupe: "estate", Entries: troupe.Encode([]troupe.Entry{revoke})})

	_, err = call(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "member"}), params)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "admin"), err.Error())
	require.Empty(t, a.Peers.(*peer.Ledger).Entries())

	_, err = call(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalNode, ID: "node-y"}), params)
	require.Error(t, err, "a node principal that did not arrive by the node door")

	out, err := call(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"}), params)
	require.NoError(t, err)
	require.Len(t, out.(rpc.TroupeEntriesResponse).Entries, 1)

	params, _ = json.Marshal(rpc.TroupeEntriesRequest{Troupe: "other"})
	_, err = call(authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"}), params)
	require.ErrorContains(t, err, "not \"other\"")
}
