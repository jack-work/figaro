package boot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/config"
)

// troupes/ belongs to the live node; a detached figaro neither lists it nor
// reads a troupe (or its sealed key) from it, though the files are there.
func TestADetachedFigaroReadsNoTroupe(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "troupes"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "troupes", "spain.toml"), []byte("url = \"wss://fig.test/\"\n"), 0o600))

	t.Setenv(config.DetachedEnv, "")
	live, err := config.Load(dir)
	require.NoError(t, err)
	names, err := TroupeNames(live)
	require.NoError(t, err)
	require.Equal(t, []string{"spain"}, names)

	t.Setenv(config.DetachedEnv, "1")
	dev, err := config.Load(dir)
	require.NoError(t, err)
	names, err = TroupeNames(dev)
	require.NoError(t, err)
	require.Empty(t, names)
	_, err = LoadTroupe(dev, "spain")
	require.ErrorContains(t, err, config.DetachedEnv)
	t.Setenv("FIGARO_TROUPE", "spain")
	_, _, err = SelectedTroupe(dev)
	require.ErrorContains(t, err, "not the node")
}
