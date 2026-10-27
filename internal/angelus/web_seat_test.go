package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

var stranger = rpc.Principal{Kind: rpc.PrincipalUser, ID: "mallory@auth.kelliher.info"}

type logTap struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logTap) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logTap) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func tapLog(t *testing.T) *logTap {
	t.Helper()
	tap := &logTap{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(tap, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return tap
}

// An authenticated subject the policy binds to no role is refused at the
// upgrade with 403, and the refusal names it in the log; a subject the
// policy seats on the same door is upgraded.
func TestWebDoorRefusesASubjectThatHoldsNoRoleAtTheUpgrade(t *testing.T) {
	tap := tapLog(t)
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Remote-User") == "mallory" {
			return authz.WithPrincipal(ctx, stranger), nil
		}
		return authz.WithPrincipal(ctx, webPrincipal), nil
	}})

	_, resp, err := dialWeb(t, path, http.Header{"Remote-User": {"mallory"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Empty(t, resp.Header.Get("WWW-Authenticate"), "a 403 is not an invitation to retry with a fresh token")
	require.Contains(t, tap.String(), "door refused upgrade")
	require.Contains(t, tap.String(), authz.PrincipalText(stranger))

	_, _, err = dialWeb(t, path, http.Header{"Remote-User": {"gluck"}})
	require.NoError(t, err)
}

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

// A daemon with no policy at all lets the owner through the web door and
// nobody else: neither at the upgrade nor at any call.
func TestWebDoorWithNoPolicySeatsOnlyTheOwner(t *testing.T) {
	owner := rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}
	_, path := unpolicedWeb(t, owner, func(ctx context.Context, r *http.Request) (context.Context, error) {
		if r.Header.Get("Remote-User") == "owner" {
			return authz.WithPrincipal(ctx, owner), nil
		}
		return authz.WithPrincipal(ctx, stranger), nil
	})

	_, resp, err := dialWeb(t, path, http.Header{"Remote-User": {"mallory"}})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	c, _, err := dialWeb(t, path, http.Header{"Remote-User": {"owner"}})
	require.NoError(t, err)
	var hello rpc.HelloResponse
	require.NoError(t, json.Unmarshal(wsCall(t, c, 1, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}), &hello))
	require.Equal(t, owner, hello.Principal)
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

// A context no listener stamped and no in-process origin stamped is not
// trusted: with no policy, and with one, a stranger on it is refused hello
// and gets no rows. A door that forgets to stamp fails closed.
func TestAnUnstampedContextIsNotTrusted(t *testing.T) {
	owner := rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}
	hello, _ := json.Marshal(rpc.HelloRequest{Version: rpc.ProtocolVersion})
	unstamped := authz.WithPrincipal(context.Background(), stranger)
	require.Equal(t, authz.DoorUnknown, authz.DoorOf(unstamped))
	rows := func() []rpc.FigaroInfoResponse { return []rpc.FigaroInfoResponse{{ID: "abcd1234"}} }

	h, _ := unpolicedWeb(t, owner, nil)
	_, err := h.angelus.Handlers[rpc.MethodHello](unstamped, hello)
	require.ErrorContains(t, err, "the unknown door passes only the owner")
	require.Empty(t, h.readable(unstamped, rows()))
	require.Error(t, h.allowed(unstamped, rpc.VerbRead, authz.Aria("abcd1234")))
	inproc := authz.WithDoor(unstamped, authz.DoorInProcess)
	_, err = h.angelus.Handlers[rpc.MethodHello](inproc, hello)
	require.NoError(t, err, "the control: the same stranger stamped in process")
	require.Len(t, h.readable(inproc, rows()), 1)

	policed := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, authz.PrincipalText(webPrincipal))}}
	policed.angelus.Handlers = policed.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: policed.hello}, policed.policy())
	_, err = policed.angelus.Handlers[rpc.MethodHello](unstamped, hello)
	require.ErrorContains(t, err, "holds no role")
	require.Empty(t, policed.readable(unstamped, rows()))
	_, err = policed.angelus.Handlers[rpc.MethodHello](inproc, hello)
	require.NoError(t, err, "the control: the same stranger stamped in process")
}

// Background work the handlers start runs in process, and says so.
func TestTheHandlersOwnContextIsInProcess(t *testing.T) {
	hs := NewHandlers(ServerConfig{Angelus: &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}, Config: &config.Loaded{}, Ctx: context.Background()})
	require.Equal(t, authz.DoorInProcess, authz.DoorOf(hs.h.ctx))
}
