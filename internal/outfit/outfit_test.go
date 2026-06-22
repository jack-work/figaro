package outfit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

func TestLoad_FlattensInlineTables(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
system = { model = "claude-x", max_tokens = 1024 }
friendly_name = "Figaro"
user_id = 7
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	assert.Equal(t, `"claude-x"`, string(patch.Set["system.model"]))
	assert.Equal(t, `1024`, string(patch.Set["system.max_tokens"]))
	assert.Equal(t, `"Figaro"`, string(patch.Set["friendly_name"]))
	assert.Equal(t, `7`, string(patch.Set["user_id"]))
}

func TestLoad_SourceChain(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "base.toml"), []byte(`
system = { model = "default-model", max_tokens = 8192 }
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
source = "base"
system = { model = "override-model" }
friendly_name = "Top"
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	assert.Equal(t, `"override-model"`, string(patch.Set["system.model"]))
	assert.Equal(t, `8192`, string(patch.Set["system.max_tokens"]))
	assert.Equal(t, `"Top"`, string(patch.Set["friendly_name"]))
}

func TestLoad_FileName_NoFrontmatter_StoresContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("# Credo\nbody"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
system = { credo = { fileName = "credo.md" } }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	var env outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["system.credo"], &env))
	assert.Equal(t, "# Credo\nbody", env.Content)
	assert.Empty(t, env.Frontmatter)
	assert.Equal(t, filepath.Join(dir, "credo.md"), env.FilePath)
}

func TestLoad_FileName_WithFrontmatter_StripsBody(t *testing.T) {
	dir := t.TempDir()
	contents := "---\nname: foo\ndescription: a foo\n---\nthe body goes here\nand keeps going"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.md"), []byte(contents), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
foo = { fileName = "foo.md" }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	var env outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["foo"], &env))
	assert.Equal(t, "name: foo\ndescription: a foo", env.Frontmatter)
	assert.Empty(t, env.Content)
	assert.Equal(t, filepath.Join(dir, "foo.md"), env.FilePath)
}

func TestLoad_DirName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0700))
	// `go` has no frontmatter; `bravo` does.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "go.md"), []byte("go body"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "bravo.md"),
		[]byte("---\nname: bravo\ndescription: B\n---\nbravo body"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
skills = { dirName = "skills" }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)

	// dirName fans entries out as dotted keys (skills.<base>) so each
	// envelope is independently visible to completion pickers.
	_, packedExists := patch.Set["skills"]
	assert.False(t, packedExists, "dirName must not produce a packed parent key")

	var goEnv outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["skills.go"], &goEnv))
	assert.Equal(t, "go body", goEnv.Content)
	assert.Empty(t, goEnv.Frontmatter)
	assert.Equal(t, filepath.Join(dir, "skills", "go.md"), goEnv.FilePath)

	var bravoEnv outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["skills.bravo"], &bravoEnv))
	assert.Equal(t, "name: bravo\ndescription: B", bravoEnv.Frontmatter)
	assert.Empty(t, bravoEnv.Content)
	assert.Equal(t, filepath.Join(dir, "skills", "bravo.md"), bravoEnv.FilePath)
}

func TestLoad_CycleDetected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "a.toml"), []byte(`source = "b"`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "b.toml"), []byte(`source = "a"`), 0600))

	_, err := outfit.New(dir).Load("a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestLoad_EmptyNameReturnsEmptyPatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`x = 1`), 0600))

	// Empty name no longer defaults to "config" — callers must
	// resolve the name (e.g. via config.DefaultLoadout) themselves.
	patch, err := outfit.New(dir).Load("")
	require.NoError(t, err)
	assert.True(t, patch.IsEmpty(), "empty loadout name must yield empty patch")
}

func TestLoad_MissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	// No loadouts/ directory at all.
	patch, err := outfit.New(dir).Load("nonexistent")
	require.NoError(t, err)
	assert.True(t, patch.IsEmpty(), "missing loadout must yield empty patch (graceful)")
}

// A subdirectory with a SKILL.md is one skill keyed by the dir name; bundled
// first-party skills merge under the user's, which override by name.
func TestLoad_DirSkill_AndBundledMerge(t *testing.T) {
	bundled := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bundled, "skills", "figaro"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "figaro", "SKILL.md"),
		[]byte("---\nname: figaro\ndescription: bundled\n---\nbody"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "figaro", "architecture.md"),
		[]byte("arch"), 0600)) // a section, NOT surfaced as its own skill
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "shared.md"),
		[]byte("bundled shared"), 0600))
	t.Setenv("FIGARO_BUNDLED_SKILLS", bundled)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "mine.md"), []byte("user mine"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "shared.md"), []byte("user shared"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"),
		[]byte("skills = { dirName = \"skills\" }\n"), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)

	// Directory-as-skill: one key from SKILL.md; sections not surfaced.
	var fig outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["skills.figaro"], &fig))
	assert.Equal(t, "name: figaro\ndescription: bundled", fig.Frontmatter)
	assert.Equal(t, filepath.Join(bundled, "skills", "figaro", "SKILL.md"), fig.FilePath)
	_, hasArch := patch.Set["skills.architecture"]
	assert.False(t, hasArch, "section files must not surface as their own skills")

	// Merge: user-only present; shared overridden by user.
	var mine, shared outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["skills.mine"], &mine))
	assert.Equal(t, "user mine", mine.Content)
	require.NoError(t, json.Unmarshal(patch.Set["skills.shared"], &shared))
	assert.Equal(t, "user shared", shared.Content, "user skill overrides bundled by name")
}
