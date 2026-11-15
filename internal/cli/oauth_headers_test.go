package cli

import (
	"github.com/BurntSushi/toml"
	"github.com/jack-work/figaro/internal/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLoginPersistsOnlyThePublicConfiguredHeader(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "providers"), 0700))
	path := filepath.Join(root, "providers", "declared.toml")
	require.NoError(t, os.WriteFile(path, []byte("preset=\"gateway\"\n[oauth]\nissuer=\"https://issuer.example\"\n[headers]\nchatgpt-account-id=\"old\"\n"), 0600))
	require.NoError(t, persistLoginHeaders(&config.Loaded{ConfigDir: root}, "declared", map[string]string{"Chatgpt-Account-Id": "account-123"}))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct{ Headers map[string]string }
	_, err = toml.Decode(string(raw), &doc)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Chatgpt-Account-Id": "account-123"}, doc.Headers)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NotContains(t, string(raw), "id_token")
}
