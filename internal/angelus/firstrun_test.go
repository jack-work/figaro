package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkrpc"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// bootAngelus starts an angelus rooted at dir with the given config.
// Returns a connected client and a teardown function. The factory is
// shared with the integration tests' mockProviderForIntegration shape.
func bootAngelus(t *testing.T, dir string, loaded *config.Loaded) (*angelus.Client, func()) {
	t.Helper()
	a := angelus.New(angelus.Config{RuntimeDir: dir})

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

	// Wait for socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)

	teardown := func() {
		acli.Close()
		cancel()
		<-errCh
	}
	return acli, teardown
}

// TestCreate_NoDefaultLoadout_ReturnsTypedError exercises the
// first-run signal: empty req.Loadout + empty config.DefaultLoadout
// must yield ErrNoDefaultLoadout with AvailableProviders populated.
func TestCreate_NoDefaultLoadout_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	loaded, err := config.Load(dir) // default_loadout unset
	require.NoError(t, err)

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, "", nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr), "expected typed jsonrpc error, got %T: %v", err, err)
	assert.Equal(t, rpc.ErrNoDefaultLoadout, jerr.Code)

	var data rpc.ErrorData
	require.NoError(t, json.Unmarshal(jerr.Data, &data))
	assert.ElementsMatch(t, []string{"mock", "anthropic"}, data.AvailableProviders)
}

// TestCreate_LoadoutMissingProvider_ReturnsTypedError exercises the
// case where the loadout resolves but lacks system.provider.
func TestCreate_LoadoutMissingProvider_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	// A loadout with no system.provider at all.
	require.NoError(t, os.WriteFile(dir+"/loadouts/bare.toml", []byte(`
[system]
model = "some-model"
`), 0600))
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	loaded.Config.DefaultLoadout = "bare"

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, "", nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, rpc.ErrNoProvider, jerr.Code)

	var data rpc.ErrorData
	require.NoError(t, json.Unmarshal(jerr.Data, &data))
	assert.Equal(t, "bare", data.Loadout)
	assert.NotEmpty(t, data.AvailableProviders)
}

// TestCreate_MissingLoadoutName_ReturnsTypedError: a default loadout
// is named but no file exists. The loadout is graceful-empty so the
// failure surfaces as ErrNoProvider (no system.provider in the empty
// patch), not ErrLoadoutNotFound.
func TestCreate_MissingLoadoutName_ReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	loaded.Config.DefaultLoadout = "ghost" // no file on disk

	acli, teardown := bootAngelus(t, dir, loaded)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = acli.Create(ctx, "", nil)
	require.Error(t, err)

	var jerr *jkrpc.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, rpc.ErrNoProvider, jerr.Code,
		"missing loadout file is graceful-empty; surfaced as no-provider")
}
