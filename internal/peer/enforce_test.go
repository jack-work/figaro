package peer_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// The node door shares the daemon's handlers, so an admitted node is a
// principal like any other: it holds what the policy binds to node:<id>
// and nothing more, and a reload changes the answer on a live link.
func TestTheNodeDoorIsDecidedByThePolicy(t *testing.T) {
	p := newPKI(t, "fed")
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })

	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: backend})
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, angelus.StoreFacts(backend))
	policy := filepath.Join(t.TempDir(), "policy.toml")
	reload := func(src string) {
		require.NoError(t, os.WriteFile(policy, []byte(src), 0o600))
		require.NoError(t, a.Policies.Reload(policy))
	}
	reload("")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: ctx}).Map

	tlsCfg, err := peer.ServerTLS(p.door("b", ""))
	require.NoError(t, err)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	door := &peer.Door{Members: members(t, p.peer("b", "a", "127.0.0.1:1"))}
	go a.Serve(ctx, tls.NewListener(raw, tlsCfg), door.Admit)

	dialer := &peer.Dialer{Members: members(t, p.peer("a", "b", raw.Addr().String()))}
	l, err := dialer.Open(ctx, "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	defer l.Close()

	outfits := func() error { _, err := l.Call(ctx, rpc.MethodOutfits, json.RawMessage(`{}`)); return err }
	create := func() error { _, err := l.Call(ctx, rpc.MethodCreate, json.RawMessage(`{}`)); return err }

	require.ErrorContains(t, outfits(), rpc.MethodOutfits, "a node no policy names was served")
	require.ErrorContains(t, create(), rpc.MethodCreate)

	reload(`
[roles.peer]
grants = [{ verbs = ["read"], on = ["*"] }]
[[bind]]
role = "peer"
principals = ["node:a"]
`)
	require.NoError(t, outfits(), "node:a holds read")
	require.ErrorContains(t, create(), rpc.MethodCreate, "read let node:a create")
}
