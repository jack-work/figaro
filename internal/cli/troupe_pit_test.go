package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
)

type stubBearer struct {
	token string
	err   error
}

func (b stubBearer) Resolve() (string, error) { return b.token, b.err }
func (b stubBearer) Invalidate(string) error  { return nil }

func troupeInput(t *testing.T) *interactiveInput {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[troupe]\nname = \"kelliher\"\n"), 0o600))
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(loaded.TroupesDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(loaded.TroupesDir(), "kelliher.toml"), []byte(`url = "wss://fig.kelliher.info"`+"\n"), 0o600))
	return &interactiveInput{loaded: loaded}
}

// A peer's form is reached through the troupe's own door for clients, with
// the caller's own troupe token; with no login the mount names the command
// that makes one, and nothing is dialled.
func TestAPeerMountUsesTheCallersTroupeLogin(t *testing.T) {
	in := troupeInput(t)
	old := troupeBearer
	t.Cleanup(func() { troupeBearer = old })

	troupeBearer = func(boot.Troupe) transport.Bearer { return stubBearer{err: errors.New("no credential")} }
	_, err := in.troupeDoor()
	require.EqualError(t, err, "figaro troupe login kelliher")
	_, _, _, _, err = in.mountForm("far/@arias")
	require.EqualError(t, err, "figaro troupe login kelliher")

	troupeBearer = func(boot.Troupe) transport.Bearer { return stubBearer{token: "the caller's own"} }
	ep, err := in.troupeDoor()
	require.NoError(t, err)
	require.Equal(t, "wss", ep.Scheme)
	require.Equal(t, "wss://fig.kelliher.info", ep.Address)
	token, err := ep.Bearer.Resolve()
	require.NoError(t, err)
	require.Equal(t, "the caller's own", token)
}
