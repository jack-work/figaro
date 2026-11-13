package angelus

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jack-work/jkrpc"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// El Inspector XIV, P2: a caller on the web endpoint is admitted no longer than
// the endpoint keeps its connection, and a proposal it parks carries that bound.
func TestAWebCallerIsAdmittedUntilTheEndpointWouldCloseIt(t *testing.T) {
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, authz.PrincipalText(webPrincipal))}}
	h.angelus.Handlers = h.endpoint(map[string]jkrpc.HandlerFunc{rpc.MethodHello: func(ctx context.Context, _ json.RawMessage) (any, error) {
		by, err := h.angelus.topologyPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		return by.Subject.(authz.TopologyCaller).Until.UnixMilli(), nil
	}}, authz.AllowAll())
	path := shortSocketPath(t)
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.angelus.ServeWeb(ctx, ln, Web{Admit: admitAs(webPrincipal), MaxConnAge: time.Hour})

	start := time.Now()
	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
	var until int64
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &until))
	require.GreaterOrEqual(t, until, start.Add(time.Hour).UnixMilli())
	require.LessOrEqual(t, until, time.Now().Add(time.Hour).UnixMilli())
}
