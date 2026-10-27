package angelus_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// fedNode is one daemon of a two-node federation on loopback: its own
// store, a peercred unix door for its owner, a TLS node door, and doors
// that admit a caller by bearer the way the web door does.
type fedNode struct {
	t       *testing.T
	a       *angelus.Angelus
	id      string
	dir     string
	owner   *sdk.Angelus
	bearer  *authz.Issuers
	keys    *authz.APIKeys
	keysAt  atomic.Int32
	issuer  *peertest.Issuer
	ctx     context.Context
	stop    context.CancelFunc
	doorLn  net.Listener
	tlsCfg  *tls.Config
	members *peer.Static

	pendingPeers []func() config.Peer
	alsoOwners   []string
	prov         provider.Provider
}

const fedPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = [%q, "user:probe@issuer.test.figaro"%s]

[roles.reader]
grants = [{ verbs = ["read"], on = ["*"] }]

[[bind]]
role = "reader"
principals = ["user:reader@issuer.test.figaro"]

[roles.self]
grants = [{ verbs = ["*"], on = ["owner:self"] }]

[[bind]]
role = "self"
principals = ["user:stranger@issuer.test.figaro"]
`

func extraPrincipals(ps []string) string {
	out := ""
	for _, p := range ps {
		out += fmt.Sprintf(", %q", p)
	}
	return out
}

func newFedPair(t *testing.T) (*fedNode, *fedNode) {
	t.Helper()
	pki := peertest.NewPKI(t, "fed")
	is := peertest.NewIssuer(t)
	a, b := openFedNode(t, is, "user:intruder@issuer.test.figaro"), openFedNode(t, is)
	a.serve(pki, b)
	b.serve(pki, a)
	a.start()
	b.start()
	return a, b
}

func openFedNode(t *testing.T, is *peertest.Issuer, alsoOwners ...string) *fedNode {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n[authz]\npolicy = \"policy.toml\"\n"), 0o600))
	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)
	return &fedNode{t: t, dir: dir, id: backend.NodeID(), issuer: is, alsoOwners: alsoOwners,
		a: angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})}
}

func (n *fedNode) serve(pki *peertest.PKI, others ...*fedNode) {
	t := n.t
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	n.doorLn = ln
	n.tlsCfg, err = peer.ServerTLS(pki.Door(n.id, ""), nil)
	require.NoError(t, err)
	for _, other := range others {
		n.pendingPeers = append(n.pendingPeers, func() config.Peer { return pki.Peer(n.id, other.id, other.doorLn.Addr().String()) })
	}
}

func (n *fedNode) start() {
	t := n.t
	var peers []config.Peer
	for _, p := range n.pendingPeers {
		peers = append(peers, p())
	}
	members, err := peer.NewStatic(0, peers, nil)
	require.NoError(t, err)
	n.members = members
	n.a.Peers = members

	loaded, err := config.Load(n.dir)
	require.NoError(t, err)
	pc, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	require.NoError(t, err)
	n.a.Peercred = pc
	require.NoError(t, os.WriteFile(n.dir+"/policy.toml", []byte(fmt.Sprintf(fedPolicy, pc.Self().String(), extraPrincipals(n.alsoOwners))), 0o600))
	n.a.Policies = authz.NewPolicies(pc.Self(), angelus.StoreFacts(n.a.Backend))
	require.NoError(t, n.a.Policies.Reload(loaded.PolicyPath()))
	n.bearer, err = authz.NewIssuers(n.issuer.Config("probe", "intruder", "reader", "stranger"), nil, nil)
	require.NoError(t, err)
	n.keys, err = authz.NewAPIKeys("fed", n.id, nil, nil)
	require.NoError(t, err)

	n.ctx, n.stop = context.WithCancel(context.Background())
	t.Cleanup(n.stop)
	n.a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: n.a,
		Config:  loaded,
		ProviderFactory: func(name string, _ provider.Knobs) (provider.Provider, error) {
			if n.prov != nil {
				return n.prov, nil
			}
			return &mockProviderForIntegration{}, nil
		},
		Ctx:           n.ctx,
		FormTemplates: template.New("t"),
	}).Map
	go n.a.Run(n.ctx)
	t.Cleanup(func() { n.a.Shutdown(0) })
	waitForAngelus(t, n.a.SocketPath)

	bearers := authz.Bearers{Issuers: n.bearer, Keys: n.keys}
	door := &peer.Door{Members: members, Verify: func(ctx context.Context, token string) (context.Context, error) {
		if apikey.Is(token) {
			n.keysAt.Add(1)
		}
		return bearers.Verify(ctx, token)
	}}
	go n.a.Serve(n.ctx, tls.NewListener(n.doorLn, n.tlsCfg), authz.DoorNode, door.Admit)

	n.owner, err = sdk.DialAngelus(transport.UnixEndpoint(n.a.SocketPath))
	require.NoError(t, err)
	t.Cleanup(func() { n.owner.Close() })
}

// callerDoor is a unix door admitting every connection as the holder of token.
func (n *fedNode) callerDoor(token string) transport.Endpoint {
	t := n.t
	path := filepath.Join(n.dir, fmt.Sprintf("caller-%d.sock", time.Now().UnixNano()))
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	go n.a.Serve(n.ctx, ln, authz.DoorLocal, func(ctx context.Context, _ net.Conn) (context.Context, error) {
		s, err := n.bearer.Open(ctx, token)
		if err != nil {
			return nil, err
		}
		return authz.WithSession(ctx, s), nil
	})
	return transport.UnixEndpoint(path)
}

// keyDoor is a unix door admitting every connection by an api key, the way
// a bearer web door does.
func (n *fedNode) keyDoor(wire string) transport.Endpoint {
	t := n.t
	path := filepath.Join(n.dir, fmt.Sprintf("key-%d.sock", time.Now().UnixNano()))
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	go n.a.Serve(n.ctx, ln, authz.DoorLocal, func(ctx context.Context, _ net.Conn) (context.Context, error) {
		return n.keys.Admit(ctx, wire)
	})
	return transport.UnixEndpoint(path)
}

func (n *fedNode) as(client string) *sdk.Angelus {
	t := n.t
	c, err := sdk.DialAngelus(n.callerDoor(n.issuer.Mint(client, time.Hour)))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func (n *fedNode) ariaAs(client, id string, onNotify sdk.NotifyHandler) *sdk.Aria {
	t := n.t
	c, err := sdk.DialAria(sdk.Addr{Door: n.callerDoor(n.issuer.Mint(client, time.Hour)), ID: id}, onNotify)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func (n *fedNode) mint() string {
	t := n.t
	created, err := n.owner.Create(n.ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	return created.FigaroID
}

func mantraOf(t *testing.T, n *fedNode, id string) string {
	t.Helper()
	c, err := sdk.DialAria(ariaAddr(n.a, id), nil)
	require.NoError(t, err)
	defer c.Close()
	resp, err := c.Form(n.ctx)
	require.NoError(t, err)
	raw, _ := resp.Snapshot.Get("mantra")
	return string(raw)
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A caller on node A reaches an aria born on node B by its id, a prefix of
// it, or its node-qualified name; the change lands in B's store, and B
// records the caller, not A, as its author.
func TestFederationRoutesToTheOwner(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)

	for i, ref := range []string{id, id[:8], b.id + "/" + id, b.id[:8] + "/" + id[:8]} {
		c := a.ariaAs("probe", ref, nil)
		want := fmt.Sprintf(`"via a %d"`, i)
		_, err := c.Set(ctx, *rawPatch(map[string]string{"mantra": want}), 0)
		require.NoError(t, err, ref)
		require.Equal(t, want, mantraOf(t, b, id), ref)
	}
	_, ok := a.a.Backend.Node(id)
	require.False(t, ok, "the aria leaked into the entry node's store")
}

// listen is proxied too: a change made at the owner reaches a caller
// following through the entry node.
func TestFederationCarriesTheListenStream(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)

	deltas := make(chan json.RawMessage, 16)
	a.ariaAs("probe", id, func(method string, params json.RawMessage) {
		if method == rpc.MethodFormDelta {
			deltas <- params
		}
	})
	at := sdk.Addr{Door: transport.UnixEndpoint(b.a.SocketPath), ID: id}
	local, err := sdk.DialAria(at, nil)
	require.NoError(t, err)
	defer local.Close()
	_, err = local.Set(ctx, *rawPatch(map[string]string{"mantra": `"said at b"`}), 0)
	require.NoError(t, err)
	for {
		select {
		case d := <-deltas:
			if strings.Contains(string(d), "said at b") {
				require.Contains(t, string(d), id)
				return
			}
		case <-ctx.Done():
			t.Fatal("the owner's change never reached the follower on a")
		}
	}
}

// ls on A shows A's rows and B's, B's marked with their node; with B's
// door down, A's rows still come back with a marker naming B.
func TestFederationListsEveryNode(t *testing.T) {
	a, b := newFedPair(t)
	here, there := a.mint(), b.mint()
	ctx := callCtx(t)

	resp, err := a.as("probe").List(ctx)
	require.NoError(t, err)
	nodes := map[string]string{}
	for _, r := range resp.Figaros {
		nodes[r.ID] = r.Node
	}
	require.Contains(t, nodes, here)
	require.Equal(t, "", nodes[here])
	require.Equal(t, b.id, nodes[there])
	require.Empty(t, resp.Unreachable)

	page, err := a.as("probe").ListWith(ctx, rpc.ListRequest{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Figaros, 1)
	require.NotEmpty(t, page.NextBefore)
	rest, err := a.as("probe").ListWith(ctx, rpc.ListRequest{Limit: 10, Before: page.NextBefore})
	require.NoError(t, err)
	require.Len(t, rest.Figaros, 1)
	require.NotEqual(t, page.Figaros[0].ID, rest.Figaros[0].ID)

	b.doorLn.Close()
	resp, err = a.as("probe").List(ctx)
	require.NoError(t, err)
	require.Len(t, resp.Unreachable, 1)
	require.Equal(t, b.id, resp.Unreachable[0].Node)
	require.Contains(t, resp.Unreachable[0].Error, "unreachable")
	ids := map[string]bool{}
	for _, r := range resp.Figaros {
		ids[r.ID] = true
	}
	require.True(t, ids[here])
}

// A fork stays on its parent's node.
func TestFederationForksOnTheParentsNode(t *testing.T) {
	a, b := newFedPair(t)
	parent := b.mint()
	ctx := callCtx(t)

	child, err := a.as("probe").Fork(ctx, parent, 0, 0, nil, nil)
	require.NoError(t, err)
	for _, id := range []string{child.Alternative, child.Continuation} {
		require.NotEmpty(t, id)
		_, ok := b.a.Backend.Node(id)
		require.True(t, ok, "%s is not on the parent's node", id)
		_, ok = a.a.Backend.Node(id)
		require.False(t, ok, "%s is on the entry node", id)
	}
	_, err = a.ariaAs("probe", child.Alternative, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"the child"`}), 0)
	require.NoError(t, err)
	require.Equal(t, `"the child"`, mantraOf(t, b, child.Alternative))
}

// The owner decides: A's policy makes intruder an owner, B's does not
// name it, so intruder rules A's arias and is refused by B on B's. B will
// not say an aria exists to a caller that may not read it, so intruder
// hears not found whether or not A already knew where the aria lives; a
// reader lacking set hears the policy. A caller without a bearer never
// leaves A.
func TestFederationTheOwnerDecides(t *testing.T) {
	a, b := newFedPair(t)
	id, mine := b.mint(), a.mint()
	ctx := callCtx(t)

	_, err := a.ariaAs("intruder", mine, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"yes"`}), 0)
	require.NoError(t, err)
	_, cold := a.ariaAs("intruder", id, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"no"`}), 0)
	require.ErrorContains(t, cold, "not found")

	_, err = a.as("probe").List(ctx)
	require.NoError(t, err)
	_, warm := a.ariaAs("intruder", id, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"no"`}), 0)
	require.Error(t, warm)
	require.Equal(t, cold.Error(), warm.Error(), "knowing the owner changed what intruder hears")
	require.NotContains(t, mantraOf(t, b, id), "no")

	_, err = a.ariaAs("reader", id, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"read only"`}), 0)
	require.ErrorContains(t, err, "user:reader@issuer.test.figaro", "a reader lacking set hears the owner's policy")
	require.NotContains(t, mantraOf(t, b, id), "read only")

	local, err := sdk.DialAria(ariaAddr(a.a, id), nil)
	require.NoError(t, err)
	defer local.Close()
	_, err = local.Set(ctx, *rawPatch(map[string]string{"mantra": `"from a's own uid"`}), 0)
	require.ErrorContains(t, err, "nodes never vouch")
}

// A forwarded request is served at the node it reached or refused there:
// B never sends A's caller on to a third hop, even back to A.
func TestFederationForwardsOnce(t *testing.T) {
	a, b := newFedPair(t)
	onA := a.mint()
	ctx := callCtx(t)

	d := &peer.Dialer{Members: a.members}
	l, err := d.Open(ctx, b.id, rpc.Passage{Forwarded: true, Bearer: a.issuer.Mint("probe", time.Hour)}, nil)
	require.NoError(t, err)
	defer l.Close()
	set := func(ref string) error {
		params, _ := json.Marshal(map[string]any{"figaro_id": ref, "patch": rawPatch(map[string]string{"mantra": `"third hop"`})})
		_, err := l.Call(ctx, rpc.MethodSet, params)
		return err
	}
	require.ErrorContains(t, set(a.id+"/"+onA), "served here or refused")
	require.Error(t, set(onA))
	require.NotContains(t, mantraOf(t, a, onA), "third hop")
}

// An owner that cannot be reached is an error naming it, whether the
// directory already knew it or had to ask.
func TestFederationNamesAnUnreachableOwner(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)

	b.doorLn.Close()
	_, err := a.ariaAs("probe", id, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"lost"`}), 0)
	require.ErrorContains(t, err, "node "+b.id+" is unreachable", "a cold miss hid the dead owner")
}

func TestFederationNamesAKnownOwnerThatDied(t *testing.T) {
	a, b := newFedPair(t)
	id := b.mint()
	ctx := callCtx(t)

	_, err := a.as("probe").List(ctx)
	require.NoError(t, err)
	b.doorLn.Close()
	_, err = a.ariaAs("probe", id, nil).Set(ctx, *rawPatch(map[string]string{"mantra": `"lost"`}), 0)
	require.ErrorContains(t, err, "node "+b.id+" is unreachable")
}
