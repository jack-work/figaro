package angelus

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
)

// A key admitted on the web door is the principal hello reports; when the
// key is revoked the door closes the connection and says why.
func TestWebDoorClosesAKeysConnectionOnRevoke(t *testing.T) {
	keys, err := authz.NewAPIKeys("estate", nil, nil)
	require.NoError(t, err)
	wire, k, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	g := authz.APIKeyGrant{ID: k.ID, Principal: "service:ci@troupe:estate", Hash: k.Hash[:], NotAfter: time.Now().Add(time.Hour)}
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
