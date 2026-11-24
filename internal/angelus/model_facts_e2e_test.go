package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type planListing struct {
	mu    sync.Mutex
	model string
	used  float64
}

func (p *planListing) Name() string        { return "mock" }
func (p *planListing) Fingerprint() string { return "mock/plan" }
func (p *planListing) SetModel(string)     {}
func (p *planListing) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}

func (p *planListing) set(model string, used float64) {
	p.mu.Lock()
	p.model, p.used = model, used
	p.mu.Unlock()
}

func (p *planListing) Models(context.Context) ([]provider.ModelInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	used := p.used
	return []provider.ModelInfo{{ID: p.model, Entry: models.Entry{
		Context:   models.Context{Default: 200_000},
		PlanUsage: &models.PlanUsage{Plan: "max", Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: &used}}},
	}}}, nil
}

func usedOn(t *testing.T, snap form.Snapshot) float64 {
	t.Helper()
	raw, ok := snap.Get(form.PlanUsageKey)
	if !ok {
		return -1
	}
	var u models.PlanUsage
	require.NoError(t, json.Unmarshal(raw, &u))
	return *u.Windows[0].UsedPercent
}

// #211: after a daemon restart every figaro is dormant, and a pager follows
// one without waking it. The catalog moving must still reach its bound form,
// and a follower arriving after the catalog moved must see the new number in
// its first read.
func TestADormantFollowedFigaroGetsTheModelFactsTheCatalogMovesTo(t *testing.T) {
	listing := &planListing{}
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	a, acli, ctx := daemonOnStore(t, dir, func(a *angelus.Angelus, be *store.XwalBackend) {
		f, err := be.ModelsForm()
		require.NoError(t, err)
		a.Models = modelcat.New(modelcat.Config{
			Build:     func(string) (provider.Provider, error) { return listing, nil },
			Providers: func() []string { return []string{"mock"} },
			Interval:  func(string) time.Duration { return time.Hour },
			Form:      f,
		})
		a.Models.Form().OnCommit(func(store.Commit) { go a.PublishModelFacts() })
	})

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	id := created.FigaroID
	bound, err := a.Backend.FormState(id)
	require.NoError(t, err)
	_, model := models.Subject(bound)
	require.NotEmpty(t, model)
	require.NoError(t, a.Registry.Kill(id))
	require.Nil(t, a.Registry.Get(id))

	listing.set(model, 64)
	require.NoError(t, a.Models.Refresh(ctx, "mock"))
	require.Never(t, func() bool { s, _ := a.Backend.FormState(id); return usedOn(t, s) >= 0 },
		100*time.Millisecond, 10*time.Millisecond, "an unwatched dormant figaro is left alone")

	deltas := make(chan rpc.FormDelta, 16)
	client := dialAria(t, a, id, func(m string, raw json.RawMessage) {
		var d rpc.FormDelta
		if m == rpc.MethodFormDelta && json.Unmarshal(raw, &d) == nil {
			deltas <- d
		}
	})
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var followed rpc.FollowResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodFollow, struct{}{}, &followed))
	var first rpc.FormResponse
	require.NoError(t, client.Call(callCtx, rpc.MethodForm, struct{}{}, &first))
	require.Equal(t, 64.0, usedOn(t, first.Snapshot), "the first read carries what the catalog moved to while nobody watched")
	raw, ok := first.Snapshot.Get(form.ContextLimitKey)
	require.True(t, ok)
	require.JSONEq(t, `200000`, string(raw))

	listing.set(model, 70)
	require.NoError(t, a.Models.Refresh(ctx, "mock"))
	require.Eventually(t, func() bool {
		select {
		case d := <-deltas:
			return usedOn(t, first.Snapshot.Apply(d.Patch)) == 70
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "the follower hears the catalog move")
	durable, err := a.Backend.FormState(id)
	require.NoError(t, err)
	require.Equal(t, 70.0, usedOn(t, durable))
	require.Nil(t, a.Registry.Get(id), "publishing did not wake it")
}
