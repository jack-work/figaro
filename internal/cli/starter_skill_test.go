package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

// First run writes an outfit and NOTHING else.
//
// It used to also drop a `howto` folder skill into the user's config, back
// when copying a file into ~/.config was the only way to have a skill at all.
// First-party skills ship inside the binary now and load from there, so that
// copy bought nothing and cost something real: a config skill shadows a
// bundled one BY NAME (internal/outfit: user config loads second and wins),
// so the copy silently outranks the shipped skill forever and drifts away
// from it. One such shadow in this repo's history ended up 201 lines behind
// on one file while holding the only copy of a section on another.
//
// The rule this pins: first run scaffolds configuration, never documentation.
func TestWriteStarterOutfitWritesNoSkills(t *testing.T) {
	cfg := t.TempDir()
	path := filepath.Join(cfg, "outfits", "starter.toml")

	require.NoError(t, writeStarterOutfit(path, "anthropic", "claude-opus-4"))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), `provider = "anthropic"`)
	require.Contains(t, string(body), `model = "claude-opus-4"`)

	// The outfit must still declare the skills table: that is what makes
	// the BUNDLED skills load. It just must not populate the directory.
	require.Contains(t, string(body), `dirName = "skills"`)

	_, err = os.Stat(filepath.Join(cfg, "skills"))
	require.True(t, os.IsNotExist(err), "first run must not create a skills directory")
}

// The skills table names a directory that does not exist, which must be a
// no-op rather than an error. Otherwise the change above would break every
// fresh install.
func TestMissingUserSkillsDirIsNotAnError(t *testing.T) {
	cfg := t.TempDir()
	path := filepath.Join(cfg, "outfits", "starter.toml")
	require.NoError(t, writeStarterOutfit(path, "anthropic", ""))

	t.Setenv("FIGARO_BUNDLED_SKILLS", "0") // isolate: bundled skills off
	patch, err := outfit.New(cfg).Load("starter")
	require.NoError(t, err)
	for k := range patch.Set {
		require.NotContains(t, k, "skills.", "no user skills exist to load")
	}
}
