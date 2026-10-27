package angelus_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

const edgePolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.guest]
grants = [
  { verbs = ["read", "listen", "send"], on = ["owner:self"] },
  { verbs = ["create"], on = ["*"] },
]

[[bind]]
role = "owner"
principals = ["user:UID", "user:admin@auth.kelliher.info"]

[[bind]]
role = "guest"
principals = ["user:ana@auth.kelliher.info"]
`

// edgeDoor stands up a daemon whose web door trusts the edge's headers, as
// spain's does, and returns the web socket path and one aria the owner
// made through the local door.
func edgeDoor(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	policy := filepath.Join(dir, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte(strings.ReplaceAll(edgePolicy, "UID", strconv.Itoa(os.Getuid()))), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte(`default_outfit = "mock"
[authz]
policy = "`+policy+`"
[authz.upstream]
issuer = "https://auth.kelliher.info"
`), 0o600))
	a, acli, ctx := daemonOnStore(t, dir, nil)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	loaded, err := config.Load(dir)
	require.NoError(t, err)
	path := filepath.Join(filepath.Dir(a.SocketPath), "web.sock")
	u, err := authz.NewUpstream(loaded.Config.Authz.Upstream, path, a.SocketPath, nil)
	require.NoError(t, err)
	ln, err := angelus.ListenUnix(path, 0o660)
	require.NoError(t, err)
	go a.ServeWeb(ctx, ln, angelus.Web{Admit: u.AdmitRequest})
	return path, created.FigaroID
}

type edgeClient struct {
	t *testing.T
	c *websocket.Conn
	n int
}

// forge dials the web door presenting whatever headers it is given, as a
// caller that reached the socket around the edge could.
func forge(t *testing.T, path string, header http.Header) (*edgeClient, *http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://fig.kelliher.info/", &websocket.DialOptions{HTTPClient: client, HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	c.SetReadLimit(64 << 20)
	t.Cleanup(func() { c.CloseNow() })
	return &edgeClient{t: t, c: c}, resp, nil
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *edgeClient) call(method string, params any) rpcReply {
	e.t.Helper()
	e.n++
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": e.n, "method": method, "params": params})
	require.NoError(e.t, err)
	require.NoError(e.t, e.c.Write(ctx, websocket.MessageText, req))
	for {
		_, msg, err := e.c.Read(ctx)
		require.NoError(e.t, err)
		var m struct {
			ID *int `json:"id"`
			rpcReply
		}
		require.NoError(e.t, json.Unmarshal(msg, &m))
		if m.ID != nil && *m.ID == e.n {
			return m.rpcReply
		}
	}
}

func onAria(id string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params[rpc.AriaKey] = id
	return params
}

// The web door end to end, with the daemon's own handlers, store, policy
// file and edge authenticator: a caller forging the edge's headers for a
// name the policy does not bind is refused at the upgrade; one forging a
// name bound to a narrow role gets in and learns nothing it may not read;
// the owner's name sees the owner's aria.
func TestTheWebDoorEndToEndAgainstForgedEdgeHeaders(t *testing.T) {
	path, owned := edgeDoor(t)

	for name, h := range map[string]http.Header{
		"no headers":                {},
		"an unknown service":        {"Remote-User": {"ci"}, "Remote-Principal": {"service"}, "Remote-Client": {"ci"}},
		"an unknown principal kind": {"Remote-User": {"admin"}, "Remote-Principal": {"node"}},
	} {
		_, resp, err := forge(t, path, h)
		require.Error(t, err, name)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
	}

	for name, h := range map[string]http.Header{
		"a name the policy does not bind": {"Remote-User": {"mallory"}},
		"the owner's uid without a scope": {"Remote-User": {strconv.Itoa(os.Getuid())}},
		"a group the policy does not use": {"Remote-User": {"mallory"}, "Remote-Groups": {"admins"}},
	} {
		_, resp, err := forge(t, path, h)
		require.Error(t, err, name)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
	}

	guest, _, err := forge(t, path, http.Header{"Remote-User": {"ana"}})
	require.NoError(t, err)
	var hello rpc.HelloResponse
	r := guest.call(rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion})
	require.Nil(t, r.Error)
	require.NoError(t, json.Unmarshal(r.Result, &hello))
	require.Equal(t, rpc.Principal{Kind: rpc.PrincipalUser, ID: "ana@auth.kelliher.info"}, hello.Principal)

	var list rpc.ListResponse
	r = guest.call(rpc.MethodList, rpc.ListRequest{})
	require.Nil(t, r.Error)
	require.NoError(t, json.Unmarshal(r.Result, &list))
	for _, row := range list.Figaros {
		require.NotEqual(t, owned, row.ID, "the guest listed the owner's aria")
	}

	var resolved rpc.NodeResolveResponse
	r = guest.call(rpc.MethodNodeResolve, rpc.NodeResolveRequest{Refs: []string{owned, owned[:8]}})
	require.Nil(t, r.Error)
	require.NoError(t, json.Unmarshal(r.Result, &resolved))
	require.Empty(t, resolved.IDs)

	for _, m := range []string{rpc.MethodContext, rpc.MethodForm, rpc.MethodQua} {
		r = guest.call(m, onAria(owned, map[string]any{"text": "witness-d00r"}))
		require.NotNil(t, r.Error, m)
		require.Contains(t, r.Error.Message, "not found", m)
	}
	r = guest.call(rpc.MethodGC, struct{}{})
	require.NotNil(t, r.Error)

	owner, _, err := forge(t, path, http.Header{"Remote-User": {"admin"}})
	require.NoError(t, err)
	r = owner.call(rpc.MethodList, rpc.ListRequest{})
	require.Nil(t, r.Error)
	require.NoError(t, json.Unmarshal(r.Result, &list))
	seen := false
	for _, row := range list.Figaros {
		seen = seen || row.ID == owned
	}
	require.True(t, seen, "the owner's name does not list the owner's aria")
	r = owner.call(rpc.MethodContext, onAria(owned, nil))
	require.Nil(t, r.Error)
}
