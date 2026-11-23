package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func loadWithProvider(t *testing.T, config, provider string) (*Loaded, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "gw.toml"), []byte(provider), 0o600))
	return Load(dir)
}

func TestCatalogRefreshDefaultsToADay(t *testing.T) {
	l, err := loadWithProvider(t, "", "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n")
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, l.CatalogRefresh("gw"))
	require.Equal(t, 24*time.Hour, l.CatalogRefresh("not-a-provider"))
	var none *Loaded
	require.Equal(t, 24*time.Hour, none.CatalogRefresh("gw"))
}

func TestCatalogRefreshIsReadPerProvider(t *testing.T) {
	l, err := loadWithProvider(t, "", "preset = \"gateway\"\ncatalog_refresh = \"15m\"\n")
	require.NoError(t, err)
	require.Equal(t, 15*time.Minute, l.CatalogRefresh("gw"))
}

func TestABadCatalogRefreshIsRefusedAtLoad(t *testing.T) {
	for value, want := range map[string]string{
		"0":      "below the minimum",
		"0s":     "below the minimum",
		"-5m":    "below the minimum",
		"30s":    "below the minimum",
		"soon":   "not a duration",
		"15":     "not a duration",
		"1 hour": "not a duration",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithProvider(t, "", "preset = \"gateway\"\ncatalog_refresh = \""+value+"\"\n")
			require.Error(t, err)
			require.Contains(t, err.Error(), want)
			require.Contains(t, err.Error(), "gw.toml")
		})
	}
}

func TestTheGlobalModelsPollIsRefusedInFavourOfCatalogRefresh(t *testing.T) {
	_, err := loadWithProvider(t, "[models]\npoll_minutes = 60\n", "preset = \"gateway\"\n")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "catalog_refresh"), err.Error())
}
