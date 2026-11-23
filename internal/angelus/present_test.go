package angelus

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/jkrpc"
)

func serveBearerWeb(t *testing.T, session *authz.Session, w Web) string {
	t.Helper()
	w.Admit = func(ctx context.Context, r *http.Request) (context.Context, error) {
		return authz.WithSession(ctx, session), nil
	}
	path := shortSocketPath(t)
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, session.Principal().String())}}
	h.angelus.Handlers = h.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello, rpc.MethodEndpointPresent: h.endpointPresent}, authz.AllowAll())
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go h.angelus.ServeWeb(ctx, ln, w)
	return path
}

func openSession(t *testing.T, is *peertest.Issuer, ttl time.Duration) *authz.Session {
	t.Helper()
	issuers, err := authz.NewIssuers(is.Config("probe"), nil, nil)
	require.NoError(t, err)
	session, err := issuers.Open(context.Background(), is.Mint("probe", ttl))
	require.NoError(t, err)
	return session
}

func TestAPresentedTokenKeepsAConnectionOpenPastTheOldExpiry(t *testing.T) {
	is := peertest.NewIssuer(t)
	session := openSession(t, is, 2*time.Second)
	oldExpiry := session.Expiry()
	path := serveBearerWeb(t, session, Web{MaxConnAge: time.Hour})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"probe"}})
	require.NoError(t, err)

	var got rpc.PresentResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodEndpointPresent, rpc.PresentRequest{Token: is.Mint("probe", time.Hour)}), &got))
	require.Greater(t, got.ExpiresMS, oldExpiry.Add(time.Minute).UnixMilli())

	time.Sleep(time.Until(oldExpiry.Add(400 * time.Millisecond)))
	var hello rpc.HelloResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 2, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &hello),
		"the connection lapsed at the token it replaced")
}

func TestPresentingATokenDoesNotOutlastTheListenersMaxAge(t *testing.T) {
	is := peertest.NewIssuer(t)
	session := openSession(t, is, time.Hour)
	path := serveBearerWeb(t, session, Web{MaxConnAge: 300 * time.Millisecond})
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"probe"}})
	require.NoError(t, err)
	wsCall(t, c, 1, rpc.MethodEndpointPresent, rpc.PresentRequest{Token: is.Mint("probe", 2*time.Hour)})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, notice, err := c.Read(ctx)
	require.NoError(t, err)
	var lapsed struct {
		Params rpc.DoorLapsed `json:"params"`
	}
	require.NoError(t, json.Unmarshal(notice, &lapsed))
	require.Equal(t, webAgeReason, lapsed.Params.Reason)
}

func TestAnyConnectionWithAnIssuerSessionLapsesAtTheTokenExpiry(t *testing.T) {
	is := peertest.NewIssuer(t)
	session := openSession(t, is, 2*time.Second)
	path := shortSocketPath(t)
	ln, err := ListenUnix(path, 0o600)
	require.NoError(t, err)
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, session.Principal().String())}}
	h.angelus.Handlers = h.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello}, authz.AllowAll())
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	admit := func(ctx context.Context, _ net.Conn) (context.Context, error) {
		return authz.WithSession(ctx, session), nil
	}
	go h.angelus.Serve(ctx, ln, authz.DoorNode, admit)

	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err, "the connection ended without saying why")
	var lapsed struct {
		Method string         `json:"method"`
		Params rpc.DoorLapsed `json:"params"`
	}
	require.NoError(t, json.Unmarshal(line, &lapsed))
	require.Equal(t, rpc.MethodDoorLapsed, lapsed.Method)
	require.Equal(t, webTokenReason, lapsed.Params.Reason)
}
