package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

const followAgePolicy = `
[roles.owner]
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:UID", "user:admin@auth.kelliher.info"]

[roles.guest]
permissions = ["read:owner:self"]

[[bind]]
role = "guest"
principals = ["user:ana@auth.kelliher.info"]

[roles.watcher]
permissions = ["listen:aria:ARIA", "read:aria:ARIA"]

[[bind]]
role = "watcher"
groups = ["watchers@auth.kelliher.info"]
`

// followAgeEndpoint is edgeEndpoint with a follow age: the owner's aria may be
// followed by anyone in the watchers group the edge names.
func followAgeEndpoint(t *testing.T, maxAge time.Duration) (path, owned string, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	policy := filepath.Join(dir, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte(strings.ReplaceAll(followAgePolicy, "UID", strconv.Itoa(os.Getuid()))), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte(`default_outfit = "mock"
[authz]
policy = "`+policy+`"
[authz.upstream]
issuer = "https://auth.kelliher.info"
`), 0o600))
	a, acli, ctx = daemonOnStore(t, dir, nil)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	owned = created.FigaroID
	body := strings.ReplaceAll(strings.ReplaceAll(followAgePolicy, "UID", strconv.Itoa(os.Getuid())), "ARIA", owned)
	require.NoError(t, os.WriteFile(policy, []byte(body), 0o600))
	require.NoError(t, a.Policies.Reload(policy))

	loaded, err := config.Load(dir)
	require.NoError(t, err)
	path = filepath.Join(filepath.Dir(a.SocketPath), "web.sock")
	u, err := authz.NewUpstream(loaded.Config.Authz.Upstream, path, a.SocketPath, nil)
	require.NoError(t, err)
	ln, err := angelus.ListenUnix(path, 0o660)
	require.NoError(t, err)
	go a.ServeWeb(ctx, ln, angelus.Web{Admit: u.AdmitRequest, MaxConnAge: time.Hour, FollowMaxAge: maxAge})
	return path, owned, a, acli, ctx
}

func (e *edgeClient) await(method string, within time.Duration) (json.RawMessage, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	for {
		_, msg, err := e.c.Read(ctx)
		if err != nil {
			return nil, err
		}
		var m struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		require.NoError(e.t, json.Unmarshal(msg, &m))
		if m.Method == method {
			return m.Params, nil
		}
	}
}

// A follow is authorized once, on the groups the edge named at the upgrade.
// At the follow age the endpoint says so and closes the connection; the
// re-follow through the edge is judged on the groups it names now, so a
// member who left the group loses the stream.
func TestAnEdgeFollowEndsAtItsAgeAndTheRefollowIsJudgedOnCurrentGroups(t *testing.T) {
	const maxAge = 400 * time.Millisecond
	path, owned, a, _, ctx := followAgeEndpoint(t, maxAge)

	watcher, _, err := forge(t, path, http.Header{"Remote-User": {"ana"}, "Remote-Groups": {"watchers"}})
	require.NoError(t, err)
	admitted := time.Now()
	r := watcher.call(rpc.MethodFollow, onAria(owned, nil))
	require.Nil(t, r.Error, "a watcher may follow")

	fcli, err := sdk.DialAria(sdk.Addr{Endpoint: transport.UnixEndpoint(a.SocketPath), ID: owned}, nil)
	require.NoError(t, err)
	defer fcli.Close()
	_, _, err = fcli.Qua(ctx, "witness-f0llow", nil)
	require.NoError(t, err)
	_, err = watcher.await(rpc.MethodAriaFrame, 5*time.Second)
	require.NoError(t, err, "the follow carried no frame before its age")

	raw, err := watcher.await(rpc.MethodEndpointLapsed, 5*time.Second)
	require.NoError(t, err, "the door never said the follow lapsed")
	require.GreaterOrEqual(t, time.Since(admitted), maxAge-50*time.Millisecond, "lapsed before its age")
	var lapsed rpc.EndpointLapsed
	require.NoError(t, json.Unmarshal(raw, &lapsed))
	require.Equal(t, maxAge.Milliseconds(), lapsed.AfterMS)
	require.Contains(t, lapsed.Reason, "follow age limit")

	_, err = watcher.await(rpc.MethodAriaFrame, 5*time.Second)
	var closed websocket.CloseError
	require.True(t, errors.As(err, &closed), "the connection outlived the lapse: %v", err)
	require.Equal(t, websocket.StatusPolicyViolation, closed.Code)
	require.Equal(t, lapsed.Reason, closed.Reason)

	left, _, err := forge(t, path, http.Header{"Remote-User": {"ana"}})
	require.NoError(t, err, "ana still holds the guest role")
	r = left.call(rpc.MethodFollow, onAria(owned, nil))
	require.NotNil(t, r.Error, "the re-follow was judged on the groups she no longer holds")

	still, _, err := forge(t, path, http.Header{"Remote-User": {"ana"}, "Remote-Groups": {"watchers"}})
	require.NoError(t, err)
	r = still.call(rpc.MethodFollow, onAria(owned, nil))
	require.Nil(t, r.Error, "a re-follow with the group still held is admitted")
}

// The unix endpoint has no edge and no follow age: a follow there outlives the
// edge's limit and keeps carrying frames.
func TestAUnixEndpointFollowOutlivesTheEdgeFollowAge(t *testing.T) {
	const maxAge = 200 * time.Millisecond
	_, owned, a, _, ctx := followAgeEndpoint(t, maxAge)

	frames := make(chan string, 64)
	follower, err := sdk.DialAria(sdk.Addr{Endpoint: transport.UnixEndpoint(a.SocketPath), ID: owned}, func(m string, _ json.RawMessage) {
		frames <- m
	})
	require.NoError(t, err)
	defer follower.Close()

	time.Sleep(3 * maxAge)
	require.False(t, follower.Lapsed())
	select {
	case <-follower.Done():
		t.Fatal("the unix door ended a follow at the edge's age")
	default:
	}

	sender, err := sdk.DialAria(sdk.Addr{Endpoint: transport.UnixEndpoint(a.SocketPath), ID: owned}, nil)
	require.NoError(t, err)
	defer sender.Close()
	_, _, err = sender.Qua(ctx, "after-the-age", nil)
	require.NoError(t, err)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-frames:
			require.NotEqual(t, rpc.MethodEndpointLapsed, m)
			if m == rpc.MethodAriaFrame {
				return
			}
		case <-deadline:
			t.Fatal("the unix follow carried no frame after the edge's age")
		}
	}
}
