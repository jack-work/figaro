package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

// First run scaffolds configuration, never documentation: a config skill
// shadows a bundled one by name.
func TestStarterOutfitBodyDeclaresNoSkills(t *testing.T) {
	cfg := t.TempDir()

	body := starterOutfitBody("anthropic", "claude-opus-4")
	require.Contains(t, string(body), `provider = "anthropic"`)
	require.Contains(t, string(body), `model = "claude-opus-4"`)

	// The skills table is what makes the bundled skills load.
	require.Contains(t, string(body), `dirName = "skills"`)

	_, err := os.Stat(filepath.Join(cfg, "skills"))
	require.True(t, os.IsNotExist(err), "first run must not create a skills directory")
}

// A skills table naming a missing directory is a no-op, or every fresh install breaks.
func TestMissingUserSkillsDirIsNotAnError(t *testing.T) {
	cfg := t.TempDir()
	path := filepath.Join(cfg, "outfits", "starter.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(starterOutfitBody("anthropic", "")), 0o600))

	t.Setenv("FIGARO_BUNDLED_SKILLS", "0")
	patch, err := outfit.New(cfg).Load("starter")
	require.NoError(t, err)
	for _, ent := range patch.Entries() {
		if ent.IsRemoval() {
			continue
		}
		k := ent.Key
		require.NotContains(t, k, "skills.", "no user skills exist to load")
	}
}
