package angelus_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

// Gluck, 11-20: provider and model are a figaro's birth values, then its own.
// Nothing an outfit does after the birth moves them; reroute is the one verb
// that re-reads them from outfits.

type routeRig struct {
	t      *testing.T
	dir    string
	a      *angelus.Angelus
	acli   *sdk.Angelus
	ctx    context.Context
	mu     sync.Mutex
	models []string
}

func newRouteRig(t *testing.T) *routeRig {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	r := &routeRig{t: t, dir: dir}
	r.outfit("mock-model")
	r.start()
	return r
}

func (r *routeRig) outfit(model string) {
	r.t.Helper()
	body := "[system]\nprovider = \"mock\"\nmodel = \"" + model + "\"\nthinking_effort = \"" + model + "-effort\"\n"
	require.NoError(r.t, os.WriteFile(r.dir+"/outfits/mock.toml", []byte(body), 0o600))
}

func (r *routeRig) start() {
	r.t.Helper()
	recording := func(cfg *angelus.ServerConfig) {
		inner := cfg.ProviderFactory
		cfg.ProviderFactory = func(name string, k provider.Knobs) (provider.Provider, error) {
			r.mu.Lock()
			r.models = append(r.models, k.Model)
			r.mu.Unlock()
			return inner(name, k)
		}
	}
	r.a, r.acli, r.ctx, _ = daemonWithProvider(r.t, r.dir, nil, func() provider.Provider { return &mockProviderForIntegration{} }, recording)
}

func (r *routeRig) restart() {
	r.t.Helper()
	r.acli.Close()
	r.a.Shutdown(0)
	r.mu.Lock()
	r.models = nil
	r.mu.Unlock()
	r.start()
}

func (r *routeRig) wake(id string) {
	r.t.Helper()
	require.NoError(r.t, promptOn(r.ctx, r.a, id, "wake"))
	waitIdle(r.t, r.a, id)
}

func (r *routeRig) born(outfits ...string) string {
	r.t.Helper()
	c, err := r.acli.Create(r.ctx, outfits, nil)
	require.NoError(r.t, err)
	return c.FigaroID
}

func (r *routeRig) key(id, key string) string {
	r.t.Helper()
	snap, err := r.a.Backend.FormState(id)
	require.NoError(r.t, err)
	if v := snap.Lookup(key); v != nil {
		return *v
	}
	return ""
}

func (r *routeRig) lastModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.models) == 0 {
		return ""
	}
	return r.models[len(r.models)-1]
}

func (r *routeRig) editOutfitAndReload() {
	r.t.Helper()
	r.outfit("edited-model")
	_, err := r.acli.OutfitReload(r.ctx)
	require.NoError(r.t, err)
	_, err = r.acli.Configure(r.ctx, rpc.ConfigureRequest{Refresh: true})
	require.NoError(r.t, err)
}

func (r *routeRig) requireRoute(id, model string) {
	r.t.Helper()
	require.Equal(r.t, "mock", r.key(id, "system.provider"))
	require.Equal(r.t, model, r.key(id, "system.model"))
}

func TestABirthRouteSurvivesAnOutfitEditReloadAndRestart(t *testing.T) {
	for name, outfits := range map[string][]string{"default": nil, "named": {"mock"}} {
		t.Run(name, func(t *testing.T) {
			r := newRouteRig(t)
			id := r.born(outfits...)
			r.editOutfitAndReload()
			r.requireRoute(id, "mock-model")
			r.restart()
			r.requireRoute(id, "mock-model")
			r.wake(id)
			require.Equal(t, "mock-model", r.lastModel(), "the restored agent was built on another model")
			require.Equal(t, "edited-model", r.key(r.born(outfits...), "system.model"), "canary: a new birth must wear the edit")
		})
	}
}

func TestARuntimeRouteSurvivesAnOutfitEditReloadAndRestart(t *testing.T) {
	r := newRouteRig(t)
	id := r.born("mock")
	ownerSets(t, r.a, id, map[string]any{"system.model": "runtime-model"})
	r.editOutfitAndReload()
	r.requireRoute(id, "runtime-model")
	r.restart()
	r.wake(id)
	r.requireRoute(id, "runtime-model")
	require.Equal(t, "runtime-model", r.lastModel())
}

func TestReapplyingAnOutfitLeavesTheRouteAlone(t *testing.T) {
	r := newRouteRig(t)
	id := r.born("mock")
	ownerSets(t, r.a, id, map[string]any{"system.model": "runtime-model", "system.thinking_effort": "high"})
	r.editOutfitAndReload()
	f, err := sdk.DialAria(ariaAddr(r.a, id), nil)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.SetDressed(r.ctx, []string{"mock"}, rpc.FormPatch{}, 0)
	require.NoError(t, err)
	r.requireRoute(id, "runtime-model")
	require.Equal(t, "high", r.key(id, "system.thinking_effort"))
}

func TestAPromptDressedInAnOutfitLeavesTheRouteAlone(t *testing.T) {
	r := newRouteRig(t)
	id := r.born("mock")
	ownerSets(t, r.a, id, map[string]any{"system.model": "runtime-model"})
	r.editOutfitAndReload()
	f, err := sdk.DialAria(ariaAddr(r.a, id), nil)
	require.NoError(t, err)
	defer f.Close()
	_, _, err = f.Qua(r.ctx, "hello", &rpc.FormInput{Outfits: []string{"mock"}})
	require.NoError(t, err)
	waitIdle(t, r.a, id)
	r.requireRoute(id, "runtime-model")
}

func TestAnExplicitPatchBesideAnOutfitStillMovesTheRoute(t *testing.T) {
	r := newRouteRig(t)
	id := r.born("mock")
	f, err := sdk.DialAria(ariaAddr(r.a, id), nil)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.SetDressed(r.ctx, []string{"mock"}, *rawPatch(map[string]string{"system.model": `"asked-model"`}), 0)
	require.NoError(t, err)
	r.requireRoute(id, "asked-model")
}

func TestAnOutfitStillGivesARouteToAFigaroBornWithout(t *testing.T) {
	r := newRouteRig(t)
	bound, err := r.acli.FormBind(r.ctx, "null", nil, nil)
	require.NoError(t, err)
	f, err := sdk.DialAria(ariaAddr(r.a, bound.FigaroID), nil)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.SetDressed(r.ctx, []string{"mock"}, rpc.FormPatch{}, 0)
	require.NoError(t, err)
	r.requireRoute(bound.FigaroID, "mock-model")
}
