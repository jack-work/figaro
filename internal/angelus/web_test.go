package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

var webPrincipal = rpc.Principal{Kind: rpc.PrincipalUser, ID: "gluck@auth.kelliher.info"}

func admitAs(p rpc.Principal) WebAdmit {
	return func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Remote-User") == "" {
			return nil, errors.New("no Remote-User")
		}
		return authz.WithPrincipal(ctx, p), nil
	}
}

// serveWeb stands the web endpoint up over hb on a unix socket, the way the
// daemon does, and returns its path.
func serveWeb(t testing.TB, hb *ariaHub, w Web) string {
	t.Helper()
	return serveWebOn(t, shortSocketPath(t), hb, w)
}

func serveWebOn(t testing.TB, path string, hb *ariaHub, w Web) string {
	t.Helper()
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, authz.PrincipalText(webPrincipal), "service:ci@troupe:estate")}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.angelus.Handlers = h.endpoint(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello}, authz.AllowAll())
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.angelus.ServeWeb(ctx, ln, w)
	return path
}

// seats is a policy binding each principal to a role, so the web endpoint
// seats it.
func seats(t testing.TB, principals ...string) *authz.Policies {
	t.Helper()
	quoted := make([]string, len(principals))
	for i, p := range principals {
		quoted[i] = strconv.Quote(p)
	}
	path := filepath.Join(t.TempDir(), "policy.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[roles.owner]
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = [`+strings.Join(quoted, ", ")+`]
`), 0o600))
	p := authz.NewPolicies(jkauthz.Principal{}, nil)
	require.NoError(t, p.Reload(path))
	return p
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

// Without an authenticator the web endpoint upgrades nobody, whatever is presented.
func TestWebEndpointWithoutAuthenticatorRefusesEveryUpgrade(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{})
	_, resp, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// A refused credential is a 401, which a client answers by invalidating its
// token and trying once more; no authenticator at all is a 403.
func TestWebEndpointRefusesWhatItsAuthenticatorRefuses(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: admitAs(webPrincipal)})
	_, resp, err := dialWeb(t, path, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The principal comes from the listener's authenticator, an aria method
// routes and follows as on the unix endpoint, and every frame is one message.
func TestWebEndpointServesTheEndpointOneFramePerMessage(t *testing.T) {
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

// The daemon's upstream authenticator on the web endpoint: the edge's
// Remote-User is the principal hello reports, and an upgrade without it is
// refused before any frame.
func TestWebEndpointAdmitsByTheEdgesHeaders(t *testing.T) {
	path := shortSocketPath(t)
	u, err := authz.NewUpstream(config.UpstreamConfig{Issuer: "https://auth.kelliher.info"}, path, "/run/figaro/angelus.sock", nil)
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
func TestWebEndpointClosesAConnectionAtItsMaxAge(t *testing.T) {
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: admitAs(webPrincipal), MaxConnAge: 200 * time.Millisecond})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, notice, err := c.Read(ctx)
	require.NoError(t, err, "the endpoint closed without saying why")
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
	var lapsed struct {
		Method string             `json:"method"`
		Params rpc.EndpointLapsed `json:"params"`
	}
	require.NoError(t, json.Unmarshal(notice, &lapsed))
	require.Equal(t, rpc.MethodEndpointLapsed, lapsed.Method)
	require.Equal(t, webAgeReason, lapsed.Params.Reason)
	_, _, err = c.Read(ctx)
	require.Error(t, err)
	require.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err), "%v", err)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce))
	require.Equal(t, webAgeReason, ce.Reason)
}

// An edge endpoint's follow age is shorter than its connection age, so it is the
// one that ends the connection, and the notice says which limit it was.
func TestTheEdgeFollowAgeEndsAConnectionBeforeItsMaxAge(t *testing.T) {
	require.Equal(t, time.Duration(0), func() time.Duration { d, _ := (Web{}).lapse(); return d }())
	for _, c := range []struct {
		w      Web
		after  time.Duration
		reason string
	}{
		{Web{MaxConnAge: 8 * time.Hour}, 8 * time.Hour, webAgeReason},
		{Web{MaxConnAge: 8 * time.Hour, FollowMaxAge: time.Hour}, time.Hour, webFollowAgeReason},
		{Web{MaxConnAge: 30 * time.Minute, FollowMaxAge: time.Hour}, 30 * time.Minute, webAgeReason},
		{Web{FollowMaxAge: time.Hour}, time.Hour, webFollowAgeReason},
	} {
		after, reason := c.w.lapse()
		require.Equal(t, c.after, after, "%+v", c.w)
		require.Equal(t, c.reason, reason, "%+v", c.w)
	}
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

// The client transport the CLI uses reaches the web endpoint through TLS, the
// way Caddy fronts it: a stale bearer is refused with 401, the client
// retries once with a fresh one, and sdk calls and pushes flow over the
// upgraded connection.
func TestWebEndpointServesTheWSSClient(t *testing.T) {
	hb := newAriaHub("abcd1234")
	agent := newFakeAgent()
	defer hb.bind(agent)()
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, authz.PrincipalText(webPrincipal))}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.angelus.Handlers = h.endpoint(nil, authz.AllowAll())
	admit := func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			return nil, errors.New("stale bearer")
		}
		return authz.WithPrincipal(ctx, webPrincipal), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewTLSServer(h.angelus.webEndpoint(ctx, Web{Admit: admit}))
	defer srv.Close()

	bearer := &countingBearer{tokens: []string{"stale", "fresh"}}
	ep := transport.WSSEndpoint("wss"+strings.TrimPrefix(srv.URL, "https"), bearer)
	ep.HTTPClient = srv.Client()
	frames := make(chan string, 8)
	c, err := sdk.DialAria(sdk.Addr{Endpoint: ep, ID: hb.id}, func(m string, _ json.RawMessage) { frames <- m })
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

// The lapse must not wait on the client: one that follows and stops reading
// backs a frame up inside the endpoint's write, and the notice queues behind it.
// The endpoint still ends the connection and drops its follows by the age plus
// the notice's grace, so the stalled client never resumes onto frames the
// old subject's follow produced after the limit.
func TestALapseEndsAConnectionWhoseClientStoppedReading(t *testing.T) {
	const maxAge = 300 * time.Millisecond
	hb := newAriaHub("abcd1234")
	path := serveWeb(t, hb, Web{Admit: admitAs(webPrincipal), FollowMaxAge: maxAge})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	wsCall(t, c, 1, rpc.MethodFollow, map[string]any{rpc.AriaKey: hb.id})
	require.Equal(t, 1, hb.Attached())
	followed := time.Now()

	frame := map[string]string{"text": strings.Repeat("x", 64<<10)}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if hb.Notify(rpc.MethodAriaFrame, frame) != nil && hb.Attached() == 0 {
				return
			}
		}
	}()

	require.Eventually(t, func() bool { return hb.Attached() == 0 }, maxAge+lapseNoticeGrace+2*time.Second, 20*time.Millisecond,
		"a client that stopped reading kept its follow past the lapse")
	require.Less(t, time.Since(followed), maxAge+lapseNoticeGrace+2*time.Second)
}

// A bearer connection ends when its token does, before any age limit, and
// says so: a follow must not outlive an expired or revoked login.
func TestABearerConnectionEndsWhenItsTokenExpires(t *testing.T) {
	is := peertest.NewIssuer(t)
	issuers, err := authz.NewIssuers(is.Config("probe"), nil, nil)
	require.NoError(t, err)
	session, err := issuers.Open(context.Background(), is.Mint("probe", 2*time.Second))
	require.NoError(t, err)

	after, reason := (Web{MaxConnAge: 8 * time.Hour, FollowMaxAge: time.Hour}).lapseFor(authz.WithSession(context.Background(), session))
	require.LessOrEqual(t, after, 2*time.Second)
	require.Equal(t, webTokenReason, reason)
	after, reason = (Web{MaxConnAge: 300 * time.Millisecond}).lapseFor(authz.WithSession(context.Background(), session))
	require.Equal(t, 300*time.Millisecond, after)
	require.Equal(t, webAgeReason, reason)

	admit := func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Remote-User") == "" {
			return nil, errors.New("no Remote-User")
		}
		return authz.WithSession(ctx, session), nil
	}
	path := shortSocketPath(t)
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, session.Principal().String())}}
	h.angelus.Handlers = h.endpoint(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello}, authz.AllowAll())
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	sctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go h.angelus.ServeWeb(sctx, ln, Web{Admit: admit, MaxConnAge: 8 * time.Hour})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, notice, err := c.Read(ctx)
	require.NoError(t, err, "the endpoint closed without saying why")
	var lapsed struct {
		Params rpc.EndpointLapsed `json:"params"`
	}
	require.NoError(t, json.Unmarshal(notice, &lapsed))
	require.Equal(t, webTokenReason, lapsed.Params.Reason)
	_, _, err = c.Read(ctx)
	require.Error(t, err)
}

// A browser that stops reading is dropped once its queue passes the bound.
// The close says why, so a page that was only slow learns to reconnect and
// resync rather than report a network fault; one that never reads again is
// cut off when the close itself times out.
func TestAWebFollowerThatFallsBehindIsClosedWithTheReason(t *testing.T) {
	setOutboxForTest(t, 256<<10, time.Minute)
	hb := newAriaHub("abcd1234")
	path := serveWeb(t, hb, Web{Admit: admitAs(webPrincipal)})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	wsCall(t, c, 1, rpc.MethodFollow, map[string]any{rpc.AriaKey: hb.id})
	require.Equal(t, 1, hb.Attached())

	frame := map[string]string{"text": strings.Repeat("x", 64<<10)}
	for range 200 {
		hb.Notify(rpc.MethodAriaFrame, frame)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, _, err = c.Read(ctx); err != nil {
			break
		}
	}
	require.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err), "%v", err)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce))
	require.Contains(t, ce.Reason, "fell behind")
	require.Eventually(t, func() bool { return hb.Attached() == 0 }, 5*time.Second, 10*time.Millisecond)
}
