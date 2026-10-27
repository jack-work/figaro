package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkrpc"
)

var stranger = rpc.Principal{Kind: rpc.PrincipalUser, ID: "mallory@auth.kelliher.info"}

func unpolicedWeb(t *testing.T, owner rpc.Principal, admit WebAdmit) (*handlers, string) {
	t.Helper()
	peer, err := authz.NewPeercred(1000, config.PeercredConfig{})
	require.NoError(t, err)
	require.Equal(t, authz.PrincipalText(owner), peer.Self().String())
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Peercred: peer}}
	h.angelus.Handlers = h.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello}, h.policy())
	path := shortSocketPath(t)
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.angelus.ServeWeb(ctx, ln, Web{Admit: admit})
	return h, path
}

// Behind the upgrade, with no policy, the handlers' own checks pass a
// stranger nothing off the local door: no rows and no verb.
func TestWithNoPolicyTheHandlersRefuseAStrangerOffTheLocalDoor(t *testing.T) {
	owner := rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}
	h, _ := unpolicedWeb(t, owner, nil)
	rows := []rpc.FigaroInfoResponse{{ID: "abcd1234"}}
	web := authz.WithDoor(authz.WithPrincipal(context.Background(), stranger), authz.DoorWeb)
	local := authz.WithDoor(authz.WithPrincipal(context.Background(), stranger), authz.DoorLocal)
	ownerWeb := authz.WithDoor(authz.WithPrincipal(context.Background(), owner), authz.DoorWeb)

	require.Empty(t, h.readable(web, append([]rpc.FigaroInfoResponse(nil), rows...)))
	require.Len(t, h.readable(local, append([]rpc.FigaroInfoResponse(nil), rows...)), 1)
	require.Len(t, h.readable(ownerWeb, append([]rpc.FigaroInfoResponse(nil), rows...)), 1)

	err := h.allowed(web, rpc.VerbRead, authz.Aria("abcd1234"))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "passes only the owner"), err.Error())
	require.NoError(t, h.allowed(local, rpc.VerbRead, authz.Aria("abcd1234")))
	require.NoError(t, h.allowed(ownerWeb, rpc.VerbRead, authz.Aria("abcd1234")))

	hello, _ := json.Marshal(rpc.HelloRequest{Version: rpc.ProtocolVersion})
	_, err = h.angelus.Handlers[rpc.MethodHello](web, hello)
	require.ErrorContains(t, err, "passes only the owner")
	_, err = h.angelus.Handlers[rpc.MethodHello](ownerWeb, hello)
	require.NoError(t, err)
}
