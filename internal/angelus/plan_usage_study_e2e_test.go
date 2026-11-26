package angelus_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type twoRoutes struct {
	mu   sync.Mutex
	used map[string]float64
}

func (p *twoRoutes) Name() string        { return "mock" }
func (p *twoRoutes) Fingerprint() string { return "mock/two" }
func (p *twoRoutes) SetModel(string)     {}
func (p *twoRoutes) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}

func (p *twoRoutes) set(model string, used float64) {
	p.mu.Lock()
	p.used[model] = used
	p.mu.Unlock()
}

func (p *twoRoutes) Models(context.Context) ([]provider.ModelInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []provider.ModelInfo
	for model, used := range p.used {
		u := used
		out = append(out, provider.ModelInfo{ID: model, Entry: models.Entry{
			PlanUsage: &models.PlanUsage{Plan: "max", Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: &u, FetchedAt: time.Now()}}},
		}})
	}
	return out, nil
}

func TestAFigaroStudyingPlanUsageOnItsRouteHearsOnlyItsRoute(t *testing.T) {
	listing := &twoRoutes{used: map[string]float64{"mock-model": 10, "other-model": 10}}
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	r := &studyRenderer{seen: map[string][]string{}}
	a, acli, ctx, _ := daemonWithProvider(t, dir, func(a *angelus.Angelus, be *store.XwalBackend) {
		catalog, err := be.ModelsForm()
		require.NoError(t, err)
		plans, err := be.PlanUsageForm()
		require.NoError(t, err)
		a.Models = modelcat.New(modelcat.Config{
			Build:     func(string) (provider.Provider, error) { return listing, nil },
			Providers: func() []string { return []string{"mock"} },
			Interval:  func(string) time.Duration { return time.Hour },
			Form:      catalog,
			Plans:     plans,
		})
	}, func() provider.Provider { return r })

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	child := created.FigaroID
	cc := dialNode(t, a, child)
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	require.NoError(t, a.Models.Refresh(callCtx, "mock"))
	mine := models.PlanUsageKey("mock", "mock-model")

	studyAndAwaitMark(t, callCtx, a, cc, child, models.PlanUsageFormID, mine)

	listing.set("other-model", 55)
	require.NoError(t, a.Models.Refresh(callCtx, "mock"))
	lib, err := a.Backend.(*store.XwalBackend).Libretto(models.PlanUsageFormID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		u := models.PlanUsageIn(lib.State(), "mock", "other-model")
		return u != nil && *u.Windows[0].UsedPercent == 55
	}, 5*time.Second, 5*time.Millisecond, "the libretto folds the other route")
	cc.turn(t, callCtx, "anything?")
	require.Empty(t, r.blocks(child, "study:"+models.PlanUsageFormID), "another route's refresh reached a figaro studying its own")

	listing.set("mock-model", 80)
	require.NoError(t, a.Models.Refresh(callCtx, "mock"))
	require.Eventually(t, func() bool {
		u := models.PlanUsageIn(lib.State(), "mock", "mock-model")
		return u != nil && *u.Windows[0].UsedPercent == 80
	}, 5*time.Second, 5*time.Millisecond)
	cc.turn(t, callCtx, "and now?")
	got := r.blocks(child, "study:"+models.PlanUsageFormID)
	require.Len(t, got, 1, "%q", got)
	require.Contains(t, got[0], "80")
	require.NotContains(t, got[0], "other-model")
}
