package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func serveStore(t *testing.T, root, runDir string) (*sdk.Angelus, func()) {
	t.Helper()
	cfg := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(cfg, 0o700))
	policy := filepath.Join(cfg, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte("[roles.owner]\npermissions = [\"*:*\"]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:"+strconv.Itoa(os.Getuid())+"\"]\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[authz]\npolicy = \""+policy+"\"\n"), 0o600))
	loaded, err := config.Load(cfg)
	require.NoError(t, err)
	backend, err := store.NewXwalBackend(filepath.Join(root, "arias"), 0)
	require.NoError(t, err)
	a := angelus.New(angelus.Config{Backend: backend, RuntimeDir: runDir})
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
	var acli *sdk.Angelus
	require.Eventually(t, func() bool {
		acli, err = sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "angelus did not come up")
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		acli.Close()
		cancel()
		<-done
		backend.Close()
	}
	t.Cleanup(stop)
	return acli, stop
}

// #211: a form pit left open across a daemon restart follows the new daemon
// and shows what it holds, rather than the last thing the old one said.
func TestAnOpenFormPitFollowsTheNextDaemon(t *testing.T) {
	root, err := os.MkdirTemp(os.Getenv("TMPDIR"), "f")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(root) })
	runDir := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o700))

	seed, err := store.NewXwalBackend(filepath.Join(root, "arias"), 0)
	require.NoError(t, err)
	label, err := seed.CreateOutfit("o", message.Patch{})
	require.NoError(t, err)
	id, err := seed.CreateConversation(label)
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	_, stop := serveStore(t, root, runDir)
	ep := sdk.Addr{Door: transport.UnixEndpoint(filepath.Join(runDir, "angelus.sock")), ID: id}
	ctx, cancel := context.WithCancel(context.Background())
	view, closeView, done, err := openFormViewOn(ctx, cancel, ep, id, "", func() {}, func() {})
	require.NoError(t, err)
	t.Cleanup(closeView)
	src, ok := view.src.(replicaSource)
	require.True(t, ok)

	stop()
	serveStore(t, root, runDir)
	next, err := sdk.DialAria(ep, nil)
	require.NoError(t, err)
	defer next.Close()
	setCtx, setCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setCancel()
	_, err = next.Set(setCtx, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"from the next daemon"`)}, nil), 0)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		v := src.Shown().Lookup("mantra")
		return v != nil && *v == "from the next daemon"
	}, 10*time.Second, 20*time.Millisecond, "the open pit still shows the old daemon")
	select {
	case <-done:
		t.Fatal("a restart ended the pit's link")
	default:
	}
}
