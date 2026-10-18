package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

var webPrincipal = rpc.Principal{Kind: rpc.PrincipalUser, ID: "gluck"}

func admitAs(p rpc.Principal) WebAdmit {
	return func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Remote-User") == "" {
			return nil, errors.New("no Remote-User")
		}
		return authz.WithPrincipal(ctx, p), nil
	}
}

// serveWeb stands the web door up over hb on a unix socket, the way the
// daemon does, and returns its path.
func serveWeb(t testing.TB, hb *ariaHub, w Web) string {
	t.Helper()
	return serveWebOn(t, shortSocketPath(t), hb, w)
}

func serveWebOn(t testing.TB, path string, hb *ariaHub, w Web) string {
	t.Helper()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.angelus.Handlers = h.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello}, authz.AllowAll())
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.angelus.ServeWeb(ctx, ln, w)
	return path
}

func dialWeb(t testing.TB, path string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://figaro/", &websocket.DialOptions{HTTPClient: client, HTTPHeader: header})
	if c != nil {
		c.SetReadLimit(webReadLimit)
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func wsCall(t *testing.T, c *websocket.Conn, id int, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := json.Marshal(params)
	require.NoError(t, err)
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
	require.NoError(t, err)
	require.NoError(t, c.Write(ctx, websocket.MessageText, req))
	for {
		typ, msg, err := c.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, websocket.MessageText, typ)
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct{ Message string }
		}
		require.NoError(t, json.Unmarshal(msg, &m), "one message is one JSON value: %q", msg)
		if m.ID != nil && *m.ID == id {
			require.Nil(t, m.Error)
			return m.Result
		}
	}
}

// Without an authenticator the web door upgrades nobody, whatever is presented.
func TestWebDoorWithoutAuthenticatorRefusesEveryUpgrade(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{})
	_, resp, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// A refused credential is a 401, which a client answers by invalidating its
// token and trying once more; no authenticator at all is a 403.
func TestWebDoorRefusesWhatItsAuthenticatorRefuses(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: admitAs(webPrincipal)})
	_, resp, err := dialWeb(t, path, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The principal comes from the listener's authenticator, an aria method
// routes and follows as on the unix door, and every frame is one message.
func TestWebDoorServesTheDoorOneFramePerMessage(t *testing.T) {
	hb := newAriaHub("abcd1234")
	agent := newFakeAgent()
	defer hb.bind(agent)()
	path := serveWeb(t, hb, Web{Admit: admitAs(webPrincipal)})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)

	var hello rpc.HelloResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &hello))
	require.Equal(t, webPrincipal, hello.Principal)

	big := strings.Repeat("x", 100<<10)
	var out map[string]any
	require.NoError(t, json.Unmarshal(wsCall(t, c, 2, rpc.MethodQua, map[string]any{rpc.AriaKey: hb.id, "text": big}), &out))
	require.Equal(t, "agent", out["served_by"])

	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 2})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for want := 1; want <= 2; want++ {
		_, msg, err := c.Read(ctx)
		require.NoError(t, err)
		var m struct {
			Method string
			Params struct {
				N  int    `json:"n"`
				ID string `json:"figaro_id"`
			}
		}
		require.NoError(t, json.Unmarshal(msg, &m), "%q", msg)
		require.Equal(t, rpc.MethodAriaFrame, m.Method)
		require.Equal(t, want, m.Params.N)
		require.Equal(t, hb.id, m.Params.ID)
	}
}

// The daemon's upstream authenticator on the web door: the edge's
// Remote-User is the principal hello reports, and an upgrade without it is
// refused before any frame.
func TestWebDoorAdmitsByTheEdgesHeaders(t *testing.T) {
	path := shortSocketPath(t)
	u, err := authz.NewUpstream(config.UpstreamConfig{}, path, "/run/figaro/angelus.sock", nil)
	require.NoError(t, err)
	path = serveWebOn(t, path, newAriaHub("abcd1234"), Web{Admit: u.AdmitRequest})

	_, resp, err := dialWeb(t, path, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	var hello rpc.HelloResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &hello))
	require.Equal(t, webPrincipal, hello.Principal)
}

// Caddy authorizes the upgrade alone, so a connection lives no longer than
// its listener allows and is told why.
func TestWebDoorClosesAConnectionAtItsMaxAge(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: admitAs(webPrincipal), MaxConnAge: 200 * time.Millisecond})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = c.Read(ctx)
	require.Error(t, err)
	require.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err), "%v", err)
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce))
	require.Equal(t, webAgeReason, ce.Reason)
}

type countingBearer struct {
	tokens      []string
	invalidated int
}

func (b *countingBearer) Resolve() (string, error) { return b.tokens[b.invalidated], nil }

func (b *countingBearer) Invalidate(string) error {
	b.invalidated++
	return nil
}

// The client transport the CLI uses reaches the web door through TLS, the
// way Caddy fronts it: a stale bearer is refused with 401, the client
// retries once with a fresh one, and sdk calls and pushes flow over the
// upgraded connection.
func TestWebDoorServesTheWSSClient(t *testing.T) {
	hb := newAriaHub("abcd1234")
	agent := newFakeAgent()
	defer hb.bind(agent)()
	h := &handlers{angelus: &Angelus{Hubs: newHubs()}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.angelus.Handlers = h.door(nil, authz.AllowAll())
	admit := func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			return nil, errors.New("stale bearer")
		}
		return authz.WithPrincipal(ctx, webPrincipal), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewTLSServer(h.angelus.webDoor(ctx, Web{Admit: admit}))
	defer srv.Close()

	bearer := &countingBearer{tokens: []string{"stale", "fresh"}}
	ep := transport.WSSEndpoint("wss"+strings.TrimPrefix(srv.URL, "https"), bearer)
	ep.HTTPClient = srv.Client()
	frames := make(chan string, 8)
	c, err := sdk.DialAria(sdk.Addr{Door: ep, ID: hb.id}, func(m string, _ json.RawMessage) { frames <- m })
	require.NoError(t, err)
	defer c.Close()
	require.Equal(t, 1, bearer.invalidated)

	callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
	defer callCancel()
	_, err = c.Form(callCtx)
	require.NoError(t, err)
	agent.push(t, rpc.MethodAriaFrame, map[string]any{"n": 1})
	select {
	case m := <-frames:
		require.Equal(t, rpc.MethodAriaFrame, m)
	case <-time.After(2 * time.Second):
		t.Fatal("no push reached the wss client")
	}
}
