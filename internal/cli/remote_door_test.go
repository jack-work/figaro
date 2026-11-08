package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

const (
	nodeGluck = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	nodeSpain = "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	nodeThird = "33333333-cccc-4ccc-8ccc-cccccccccccc"
)

type remoteRig struct {
	t      *testing.T
	cfg    string
	nodes  map[string]string
	mu     sync.Mutex
	probes []transport.Endpoint
	signed map[string]bool
}

func newRemoteRig(t *testing.T, config string, profiles map[string]string) *remoteRig {
	t.Helper()
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_STATE_DIR", state)
	t.Setenv("FIGARO_CACHE_DIR", t.TempDir())
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_ARIA", "")
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(config), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "troupes"), 0o700))
	for name, url := range profiles {
		body := `url = "` + url + `"` + "\n[auth]\nscheme = \"oidc-device\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(cfg, "troupes", name+".toml"), []byte(body), 0o600))
	}
	r := &remoteRig{t: t, cfg: cfg, nodes: map[string]string{}, signed: map[string]bool{}}
	for name := range profiles {
		r.signed[name] = true
	}
	oldBearer, oldProbe := troupeBearer, probeTroupeNode
	t.Cleanup(func() {
		troupeBearer, probeTroupeNode = oldBearer, oldProbe
		forgetDoors()
	})
	troupeBearer = func(tr boot.Troupe) transport.Bearer {
		if !r.signed[tr.Name] {
			return stubBearer{err: errors.New("no credential")}
		}
		return stubBearer{token: tr.Name}
	}
	probeTroupeNode = func(ep transport.Endpoint) (string, error) {
		r.mu.Lock()
		r.probes = append(r.probes, ep)
		r.mu.Unlock()
		node, ok := r.nodes[ep.Address]
		if !ok {
			return "", errors.New("unreachable")
		}
		return node, nil
	}
	forgetDoors()
	return r
}

func forgetDoors() {
	doorMu.Lock()
	doorCached = nil
	doorMu.Unlock()
	nodeDoorMu.Lock()
	nodeDoorCached, ownNodeCached = nil, nil
	nodeDoorMu.Unlock()
}

func (r *remoteRig) own(node string) {
	r.t.Helper()
	require.NoError(r.t, os.MkdirAll(boot.AriaRoot(), 0o700))
	require.NoError(r.t, os.WriteFile(filepath.Join(boot.AriaRoot(), "node-id"), []byte(node+"\n"), 0o600))
}

func (r *remoteRig) probeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.probes)
}

func bearerOf(t *testing.T, ep transport.Endpoint) string {
	t.Helper()
	require.NotNil(t, ep.Bearer)
	tok, err := ep.Bearer.Resolve()
	require.NoError(t, err)
	return tok
}

func TestAnIDOnAnotherNodeGoesToTheDoorThatAnswersForIt(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example", "third": "wss://third.example"})
	r.own(nodeGluck)
	r.nodes["wss://spain.example"] = nodeSpain
	r.nodes["wss://third.example"] = nodeThird

	a, err := doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://spain.example", a.Door.Address)
	require.Equal(t, "spain", bearerOf(t, a.Door))
	require.Equal(t, nodeSpain+"/abc", a.ID)

	a, err = doorFor(nodeThird[:8] + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://third.example", a.Door.Address)
	require.Equal(t, "third", bearerOf(t, a.Door))
}

func TestAnIDOnThisNodeOrNoNodeStaysLocal(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example"})
	r.own(nodeGluck)
	r.nodes["wss://spain.example"] = nodeSpain
	for _, id := range []string{"abc", nodeGluck + "/abc", nodeGluck[:8] + "/abc"} {
		a, err := doorFor(id)
		require.NoError(t, err, id)
		require.Equal(t, "unix", a.Door.Scheme, id)
		require.Equal(t, boot.AngelusSocketPath(), a.Door.Address, id)
	}
	require.Zero(t, r.probeCount(), "a local id probes no door")
}

func TestAConfiguredTroupeStillTakesEveryID(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example", "third": "wss://third.example"})
	r.own(nodeGluck)
	r.nodes["wss://third.example"] = nodeThird
	t.Setenv("FIGARO_TROUPE", "spain")
	a, err := doorFor(nodeThird + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://spain.example", a.Door.Address)
	require.Zero(t, r.probeCount())
}

// A door's answer picks the profile, never where a token goes: each probe
// and each route carries the bearer of the profile whose URL it dials, even
// when two doors claim one node.
func TestABearerNeverGoesToAnotherProfilesURL(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"honest": "wss://honest.example", "liar": "wss://liar.example"})
	r.own(nodeGluck)
	r.nodes["wss://honest.example"] = nodeSpain
	r.nodes["wss://liar.example"] = nodeSpain

	_, err := doorFor(nodeSpain + "/abc")
	require.ErrorContains(t, err, "answers at 2 doors")
	require.NotZero(t, r.probeCount())
	for _, ep := range r.probes {
		want := map[string]string{"wss://honest.example": "honest", "wss://liar.example": "liar"}[ep.Address]
		require.Equal(t, want, bearerOf(t, ep), "probe of %s", ep.Address)
	}

	r.nodes["wss://liar.example"] = nodeThird
	forgetDoors()
	require.NoError(t, os.Remove(troupeDoorsPath()))
	a, err := doorFor(nodeThird + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://liar.example", a.Door.Address)
	require.Equal(t, "liar", bearerOf(t, a.Door))
	a, err = doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://honest.example", a.Door.Address)
	require.Equal(t, "honest", bearerOf(t, a.Door))
}

func TestProfilesOnOneURLAreOneDoorAndTheNodesOwnTroupeSpeaks(t *testing.T) {
	r := newRemoteRig(t, "[troupe]\nname = \"spain\"\n", map[string]string{"kelliher": "wss://fig.example", "spain": "wss://fig.example"})
	r.own(nodeGluck)
	r.nodes["wss://fig.example"] = nodeSpain
	a, err := doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, "spain", bearerOf(t, a.Door))
	require.Equal(t, 1, r.probeCount())

	r.signed["spain"] = false
	forgetDoors()
	a, err = doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, "kelliher", bearerOf(t, a.Door), "a signed-in profile on the same URL serves")
}

func TestTheNodeIDIsCachedAndReprobedOnAMiss(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example"})
	r.own(nodeGluck)
	r.nodes["wss://spain.example"] = nodeSpain
	_, err := doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, 1, r.probeCount())

	forgetDoors()
	_, err = doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Equal(t, 1, r.probeCount(), "the cache answers a later process")

	r.nodes["wss://spain.example"] = nodeThird
	forgetDoors()
	a, err := doorFor(nodeThird + "/abc")
	require.NoError(t, err)
	require.Equal(t, "wss://spain.example", a.Door.Address)
	require.Equal(t, 2, r.probeCount(), "a miss probes again")
	require.Equal(t, nodeThird, readDoorCache()["wss://spain.example"].Node)
}

func TestANodeNoProfileReachesIsRefused(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example"})
	r.own(nodeGluck)
	r.nodes["wss://spain.example"] = nodeSpain
	_, err := doorFor(nodeThird + "/abc")
	require.EqualError(t, err, "no troupe profile reaches node "+nodeThird)
}

func TestALapsedLoginIsOneLine(t *testing.T) {
	r := newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example"})
	r.own(nodeGluck)
	r.nodes["wss://spain.example"] = nodeSpain
	r.signed["spain"] = false
	_, err := doorFor(nodeSpain + "/abc")
	require.EqualError(t, err, "figaro troupe login spain")

	r.signed["spain"] = true
	forgetDoors()
	probeTroupeNode = func(ep transport.Endpoint) (string, error) {
		return "", errors.New("wss://spain.example: upgrade refused: HTTP 401")
	}
	_, err = doorFor(nodeSpain + "/abc")
	require.EqualError(t, err, "figaro troupe login spain")
}

// Reading this node's id must not mint one, and needs no daemon.
func TestTheOwnNodeReadWritesNothing(t *testing.T) {
	newRemoteRig(t, "", map[string]string{"spain": "wss://spain.example"})
	require.Equal(t, "", ownNode())
	_, err := os.Stat(filepath.Join(boot.AriaRoot(), "node-id"))
	require.True(t, os.IsNotExist(err), "node-id must not be created: %v", err)
}

// A login the door stops taking after the node was learned still ends in
// the one line that renews it.
func TestADoorRefusingTheLoginNamesTheLoginCommand(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	url := "wss://" + strings.TrimPrefix(srv.URL, "https://") + "/"
	r := newRemoteRig(t, "", map[string]string{"spain": url})
	r.own(nodeGluck)
	require.NoError(t, writeDoorCache(map[string]doorRecord{url: {Node: nodeSpain}}))
	old := troupeHTTPClient
	troupeHTTPClient = srv.Client()
	t.Cleanup(func() { troupeHTTPClient = old })

	a, err := doorFor(nodeSpain + "/abc")
	require.NoError(t, err)
	require.Zero(t, r.probeCount())
	_, err = sdk.DialAria(a, nil)
	require.EqualError(t, err, "figaro troupe login spain")
}
