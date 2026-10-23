package angelus_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type countedListing struct {
	calls *atomic.Int32
}

func (p countedListing) Name() string        { return "counted" }
func (p countedListing) Fingerprint() string { return "counted/v1" }
func (p countedListing) SetModel(string)     {}
func (p countedListing) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}
func (p countedListing) Models(context.Context) ([]provider.ModelInfo, error) {
	p.calls.Add(1)
	return []provider.ModelInfo{{ID: "gpt-4.1", ContextWindow: 1_047_576}}, nil
}

// A cold daemon on a store that already holds the catalog answers `figaro
// models` with rows at once, and does not list a provider it listed recently.
func TestAColdDaemonAnswersModelsFromTheStoreWithoutListing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	var calls atomic.Int32
	build := func(string) (provider.Provider, error) { return countedListing{calls: &calls}, nil }

	seedBackend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)
	seedForm, err := seedBackend.ModelsForm()
	require.NoError(t, err)
	seed := modelcat.New(modelcat.Config{Build: build, Form: seedForm})
	require.NoError(t, seed.Refresh(context.Background(), "openai"))
	_, seeded := seedForm.Snapshot()
	require.NoError(t, seedBackend.Close())
	require.EqualValues(t, 1, calls.Load())

	a, acli, ctx := daemonOnStore(t, dir, func(a *angelus.Angelus, be *store.XwalBackend) {
		f, err := be.ModelsForm()
		require.NoError(t, err)
		a.Models = modelcat.New(modelcat.Config{
			Build:     build,
			Providers: func() []string { return []string{"openai"} },
			Poll:      time.Hour,
			Form:      f,
		})
	})
	go a.Models.Run(ctx)

	_, err = acli.Attach(ctx, models.FormID)
	require.NoError(t, err)
	conn, err := sdk.DialAria(ariaAddr(a, models.FormID), nil)
	require.NoError(t, err)
	defer conn.Close()
	resp, err := conn.Form(ctx)
	require.NoError(t, err)
	require.Equal(t, seeded, resp.Version, "the version a client mirrors survives the restart")
	require.Equal(t, 1_047_576, models.EntryLimit(resp.Snapshot, "openai", "gpt-4.1", ""))

	require.Never(t, func() bool { return calls.Load() > 1 }, 100*time.Millisecond, 5*time.Millisecond,
		"a fresh provider was listed again on start")
}
