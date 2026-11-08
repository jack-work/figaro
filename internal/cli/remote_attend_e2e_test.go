package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type remotePair struct {
	local    *sdk.Angelus
	loaded   *config.Loaded
	far      string
	farAria  string
	edge     *edgeStub
	localDir string
}

// remotePair is two real angelus: this node's own over a unix socket, which
// counts the far node as a troupe member and cannot reach it, and the far
// node behind a stub edge reached only through troupes/far.toml.
func newRemotePair(t *testing.T) *remotePair {
	t.Helper()
	_, edge, stub, farAria := edgeServed(t, time.Hour)
	farNode := nodeOfEdge(t, edge)

	r := newRemoteRig(t, "", map[string]string{"far": "wss://fig.test/"})
	probeTroupeNode = func(ep transport.Endpoint) (string, error) {
		if tok, _ := ep.Bearer.Resolve(); tok != "far" || ep.Address != "wss://fig.test/" {
			return "", fmt.Errorf("probe carried %q to %s", tok, ep.Address)
		}
		acli, err := sdk.DialAngelus(ep)
		if err != nil {
			return "", err
		}
		defer acli.Close()
		h, err := acli.Hello(context.Background())
		if err != nil {
			return "", err
		}
		return h.Node, nil
	}
	checkRoutedDoor = r.realCheck
	old := troupeHTTPClient
	troupeHTTPClient = edge.HTTPClient
	t.Cleanup(func() { troupeHTTPClient = old })

	require.NoError(t, os.MkdirAll(boot.AriaRoot(), 0o700))
	backend, err := store.NewXwalBackend(boot.AriaRoot(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { backend.Close() })
	pki := peertest.NewPKI(t, "fed")
	members, err := peer.NewStatic(0, []config.Peer{pki.Peer(backend.NodeID(), farNode, "127.0.0.1:1")}, nil)
	require.NoError(t, err)

	policy := filepath.Join(r.cfg, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte("[roles.owner]\ngrants = [{ verbs = [\"*\"], on = [\"*\"] }]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:"+strconv.Itoa(os.Getuid())+"\"]\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.cfg, "config.toml"), []byte("[authz]\npolicy = \""+policy+"\"\n"), 0o600))
	loaded, err := config.Load(r.cfg)
	require.NoError(t, err)

	runtime := boot.AngelusRuntimeDir()
	a := angelus.New(angelus.Config{Backend: backend, RuntimeDir: runtime})
	a.Peers = members
	pc, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	require.NoError(t, err)
	a.Peercred = pc
	a.Policies = authz.NewPolicies(pc.Self(), angelus.StoreFacts(backend))
	require.NoError(t, a.Policies.Reload(policy))
	ctx, cancel := context.WithCancel(context.Background())
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a, Config: loaded, Ctx: ctx,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) { return nil, nil },
	}).Map
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	var local *sdk.Angelus
	require.Eventually(t, func() bool {
		local, err = sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "local angelus did not come up")
	t.Cleanup(func() { local.Close() })
	forgetDoors()
	return &remotePair{local: local, loaded: loaded, far: farNode, farAria: farAria, edge: stub, localDir: runtime}
}

func nodeOfEdge(t *testing.T, ep transport.Endpoint) string {
	t.Helper()
	acli, err := sdk.DialAngelus(ep)
	require.NoError(t, err)
	defer acli.Close()
	h, err := acli.Hello(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, h.Node)
	return h.Node
}

// attend binds this shell to an aria on another node through that node's
// own door, and every later read of the binding (plain figaro, listen, a
// bare prompt) reaches the aria there; ls stays on this node.
func TestAttendAnAriaOnAnotherNodeAndReachItThroughItsDoor(t *testing.T) {
	p := newRemotePair(t)
	ctx := callCtxCLI(t)
	pid := os.Getpid()
	oldForce, oldPID := forceBind, shellPID
	forceBind, shellPID = true, pid
	t.Cleanup(func() { forceBind, shellPID = oldForce, oldPID })
	env := verbEnv{loaded: p.loaded, acli: p.local, shellPID: pid}

	out, err := attendVerb(ctx, env, p.far[:8]+"/"+p.farAria[:8])
	require.NoError(t, err)
	require.Equal(t, p.far[:8]+"/"+p.farAria, out.ID)
	require.Equal(t, "wss://fig.test/", out.EP.Door.Address)

	r, err := p.local.Resolve(ctx, pid)
	require.NoError(t, err)
	require.True(t, r.Found)
	require.Equal(t, p.far+"/"+p.farAria, r.FigaroID, "the local binding holds the whole node and id")

	id, addr, err := resolveFigaroTargetEndpoint(ctx, p.loaded, p.local, "", false, dressing{})
	require.NoError(t, err)
	require.Equal(t, p.far+"/"+p.farAria, id)
	require.Equal(t, "wss://fig.test/", addr.Door.Address)
	before := p.edge.upgrades.Load()
	fcli, err := sdk.DialAria(addr, nil)
	require.NoError(t, err)
	defer fcli.Close()
	_, err = fcli.Form(ctx)
	require.NoError(t, err, "the far aria answers through its door")
	require.Greater(t, p.edge.upgrades.Load(), before)

	list, err := p.local.List(ctx)
	require.NoError(t, err)
	for _, f := range list.Figaros {
		require.NotEqual(t, p.farAria, f.ID, "ls stays local")
	}

	_, err = attendVerb(ctx, env, p.far[:8]+"/"+p.farAria+":1")
	require.ErrorContains(t, err, "is on another node: attend it at its head")
	_, err = attendVerb(ctx, env, ":1")
	require.ErrorContains(t, err, "is on another node: attend it at its head")
	_, err = forkVerb(ctx, env, forkPlan{}, bindBranch)
	require.ErrorContains(t, err, "is on another node: forking there is not supported yet")
}

func callCtxCLI(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
