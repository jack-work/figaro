package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/jkrpc"
)

func bootAngelus(t *testing.T, dir string, loaded *config.Loaded) (*sdk.Angelus, func()) {
	t.Helper()
	a := angelus.New(angelus.Config{Backend: store.NewTestBackend(t), RuntimeDir: testRuntimeDir(t, dir)})

	factory := func(providerName string, knobs provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus:            a,
		Config:             loaded,
		ProviderFactory:    factory,
		AvailableProviders: []string{"mock", "anthropic"},
		Ctx:                ctx,
	}).Map

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	waitForAngelus(t, a.SocketPath)

	acli, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)

	teardown := func() {
		acli.Close()
		cancel()
		<-errCh
	}
	return acli, teardown
}

func TestCreate_NoDefaultOutfit_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	loaded, err := config.Load(dir) // default_outfit unset
	require.NoError(t, err)

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, nil, nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr), "expected typed jsonrpc error, got %T: %v", err, err)
	assert.Equal(t, rpc.ErrNoDefaultOutfit, jerr.Code)

	var data rpc.ErrorData
	require.NoError(t, json.Unmarshal(jerr.Data, &data))
	assert.ElementsMatch(t, []string{"mock", "anthropic"}, data.AvailableProviders)
}

func TestCreate_OutfitMissingProvider_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/bare.toml", []byte(`
[system]
model = "some-model"
`), 0600))
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	loaded.Config.DefaultOutfit = "bare"

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, nil, nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, rpc.ErrNoProvider, jerr.Code)

	var data rpc.ErrorData
	require.NoError(t, json.Unmarshal(jerr.Data, &data))
	assert.Equal(t, "bare", data.Outfit)
	assert.NotEmpty(t, data.AvailableProviders)
}

// A named default with no file is graceful-empty: ErrNoProvider, not ErrOutfitNotFound.
func TestCreate_MissingOutfitName_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	loaded.Config.DefaultOutfit = "ghost" // no file on disk

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, nil, nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, rpc.ErrNoProvider, jerr.Code,
		"missing outfit file is graceful-empty; surfaced as no-provider")
}
