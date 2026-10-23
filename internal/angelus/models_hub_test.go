package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
)

type listingProvider struct{ list []provider.ModelInfo }

func (p listingProvider) Name() string        { return "listing" }
func (p listingProvider) Fingerprint() string { return "listing/v1" }
func (p listingProvider) SetModel(string)     {}
func (p listingProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}
func (p listingProvider) Models(context.Context) ([]provider.ModelInfo, error) { return p.list, nil }

func modelsHandlers(t *testing.T) (*handlers, *modelcat.Catalog) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fmh")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	cat := modelcat.New(modelcat.Config{Build: func(string) (provider.Provider, error) {
		return listingProvider{list: []provider.ModelInfo{{ID: "gpt-4.1", ContextWindow: 1_047_576}, {ID: "claude-z", ContextWindow: 1_047_576}}}, nil
	}})
	a := &Angelus{Hubs: newHubs(), RuntimeDir: dir, Models: cat}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); a.Hubs.closeAll() })
	h := &handlers{angelus: a, ctx: ctx}
	h.publishCatalogs()
	return h, cat
}

// A client attaches to @models like any form: a seed read, deltas as the
// catalog fills, and a Since read that answers with patches, not the snapshot.
func TestModelsCatalogIsServedLikeAForm(t *testing.T) {
	h, cat := modelsHandlers(t)
	require.NoError(t, h.requireAria(models.FormID))
	hb := h.hubFor(models.FormID)
	client, frames := dialHubOn(t, serveDoor(t, h, authz.AllowAll()), hb)
	require.Eventually(t, func() bool { return hb.Attached() == 1 }, 2*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var seed rpc.FormResponse
	require.NoError(t, client.Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &seed))
	require.False(t, seed.Delta)
	require.Equal(t, 0, seed.Snapshot.Len())

	cat.Want("openai", "gpt-4.1")
	var delta rpc.FormDelta
	select {
	case f := <-frames:
		require.Equal(t, rpc.MethodFormDelta, f.method)
		require.NoError(t, json.Unmarshal(f.params, &delta))
	case <-time.After(2 * time.Second):
		t.Fatal("no delta reached the attached client")
	}
	require.Equal(t, models.FormID, delta.AriaID)
	mirror := seed.Snapshot.Apply(delta.Patch)
	require.Equal(t, 1_047_576, models.EntryLimit(mirror, "openai", "gpt-4.1", ""))

	var at rpc.FormResponse
	require.NoError(t, client.Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &at))
	require.False(t, at.Delta, "a read with no Since is a snapshot")
	require.Equal(t, delta.Version, at.Version)

	cat.Want("anthropic", "claude-z")
	require.Eventually(t, func() bool { return cat.Limit("anthropic", "claude-z") == 1_047_576 }, 2*time.Second, 5*time.Millisecond)

	var since rpc.FormResponse
	require.NoError(t, client.Call(ctx, rpc.MethodForm, rpc.FormRequest{Since: at.Version}, &since))
	require.True(t, since.Delta)
	require.Zero(t, since.Snapshot.Len())
	require.NotEmpty(t, since.Patches)
	caught := at.Snapshot
	for _, p := range since.Patches {
		caught = caught.Apply(p.Patch)
	}
	require.Equal(t, 1_047_576, models.EntryLimit(caught, "anthropic", "claude-z", ""))
	require.Equal(t, 1_047_576, models.EntryLimit(caught, "openai", "gpt-4.1", ""))

	var up rpc.FormResponse
	require.NoError(t, client.Call(ctx, rpc.MethodForm, rpc.FormRequest{Since: since.Version}, &up))
	require.True(t, up.Delta)
	require.Empty(t, up.Patches)
}

func TestModelsCatalogRefusesWrites(t *testing.T) {
	h, _ := modelsHandlers(t)
	client, _ := dialDoor(t, serveDoor(t, h, authz.AllowAll()), models.FormID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out rpc.SetResponse
	err := client.Call(ctx, rpc.MethodSet, rpc.SetRequest{}, &out)
	require.ErrorContains(t, err, "read-only")
	var qua map[string]any
	err = client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &qua)
	require.Error(t, err, "a catalog has no turn loop to wake")
}

func TestModelsWantKicksARefresh(t *testing.T) {
	h, cat := modelsHandlers(t)
	params, _ := json.Marshal(rpc.ModelsWantRequest{Provider: "openai", Model: "gpt-4.1"})
	_, err := h.modelsWant(context.Background(), params)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return cat.Limit("openai", "gpt-4.1") == 1_047_576 }, 2*time.Second, 5*time.Millisecond)
}

// A waiting refresh answers after the listing has landed, so the caller's next
// read of @models already holds it.
func TestModelsRefreshAnswersAfterTheListingLands(t *testing.T) {
	h, cat := modelsHandlers(t)
	params, _ := json.Marshal(rpc.ModelsRefreshRequest{Providers: []string{"openai"}, Wait: true})
	resp, err := h.modelsRefresh(context.Background(), params)
	require.NoError(t, err)
	require.True(t, resp.(rpc.ModelsRefreshResponse).Done)
	e, ok := models.Lookup(cat.Snapshot(), "openai", "claude-z")
	require.True(t, ok)
	require.Equal(t, 1_047_576, e.Context.Default)
}

func TestModelsRefreshIsAnAdminVerb(t *testing.T) {
	verb, ok := rpc.VerbOf(rpc.MethodModelsRefresh)
	require.True(t, ok)
	require.Equal(t, rpc.VerbAdmin, verb, "a refresh spends the daemon's provider credentials")
}
