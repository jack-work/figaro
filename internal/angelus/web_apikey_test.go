package angelus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
)

// A key admitted on the web endpoint is the principal hello reports; when the
// key is revoked the endpoint closes the connection and says why.
func TestWebEndpointClosesAKeysConnectionOnRevoke(t *testing.T) {
	keys, err := authz.NewAPIKeys("estate", "node-a", nil, nil)
	require.NoError(t, err)
	wire, k, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	g := authz.APIKeyGrant{ID: k.ID, Principal: "service:ci@troupe:estate", Hash: k.Hash[:], NotAfter: time.Now().Add(time.Hour), Nodes: []string{"node-a"}}
	require.NoError(t, keys.SetTroupe([]authz.APIKeyGrant{g}))
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: authz.Bearers{Keys: keys}.AdmitRequest})

	_, resp, err := dialWeb(t, path, http.Header{"Authorization": {"Bearer fgk_not_a_key"}})
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	c, _, err := dialWeb(t, path, http.Header{"Authorization": {"Bearer " + wire}})
	require.NoError(t, err)
	var hello rpc.HelloResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &hello))
	require.Equal(t, "ci@troupe:estate", hello.Principal.ID)

	g.Void = "revoked"
	require.NoError(t, keys.SetTroupe([]authz.APIKeyGrant{g}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	require.Error(t, err)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, websocket.StatusPolicyViolation, ce.Code)
	require.Contains(t, ce.Reason, "revoked")
}

// The revoke test flaked with "failed to read frame header: EOF": the endpoint's
// writes ran under the connection's life, and the websocket library closes
// the socket outright when a write's context ends. A revoke that landed while
// a write was in flight (or before the library had cleared its write timer)
// dropped the connection with no close frame, so a revoked client saw EOF
// instead of the reason. Whether the revoke lands mid-write (the usual case
// here) or before the write starts, the client hears the reason: the whole
// frame then the close, or the close alone, never EOF.
func TestARevokeDuringAWriteStillClosesWithTheReason(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { client.CloseNow() })
	client.SetReadLimit(-1)
	server := <-accepted

	life, end := context.WithCancelCause(context.Background())
	w, release := newWebConn(context.Background(), life, server)
	defer release()
	big := append(bytes.Repeat([]byte("x"), 16<<20), '\n')
	wrote := make(chan error, 1)
	go func() {
		_, err := w.Write(big)
		wrote <- err
	}()
	time.Sleep(100 * time.Millisecond)
	end(errors.New("api key revoked"))
	go func() {
		<-wrote
		_ = w.Close()
	}()

	_, msg, err := client.Read(ctx)
	if err == nil {
		require.Len(t, msg, len(big)-1, "a partial frame reached the client")
		_, _, err = client.Read(ctx)
	}
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce), "the revoked client saw %v, not the reason", err)
	require.Equal(t, websocket.StatusPolicyViolation, ce.Code)
	require.Contains(t, ce.Reason, "revoked")
}
