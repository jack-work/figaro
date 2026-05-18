package outfit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers", "anthropic"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "anthropic", "config.toml"), []byte(`
system = { model = "default-model", max_tokens = 8192 }
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`
source = "anthropic"
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
	var skills map[string]outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(patch.Set["skills"], &skills))

	assert.Equal(t, "go body", skills["go"].Content)
	assert.Empty(t, skills["go"].Frontmatter)
	assert.Equal(t, filepath.Join(dir, "skills", "go.md"), skills["go"].FilePath)

	assert.Equal(t, "name: bravo\ndescription: B", skills["bravo"].Frontmatter)
	assert.Empty(t, skills["bravo"].Content)
	assert.Equal(t, filepath.Join(dir, "skills", "bravo.md"), skills["bravo"].FilePath)
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

func TestLoad_DefaultsToConfigName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadouts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadouts", "config.toml"), []byte(`x = 1`), 0600))

	patch, err := outfit.New(dir).Load("")
	require.NoError(t, err)
	assert.Equal(t, `1`, string(patch.Set["x"]))
}

func TestBootstrap_TemplatesCredo(t *testing.T) {
	dir := t.TempDir()
	o := outfit.New(dir)
	cb := snap(`{"system.credo":"hello {{.Provider}} {{.FigaroID}}"}`)

	patch, err := o.Bootstrap(cb, outfit.BootCtx{Provider: "anthropic", FigaroID: "abc123"})
	require.NoError(t, err)
	assert.Equal(t, `"hello anthropic abc123"`, string(patch.Set["system.prompt"]))
}

func TestBootstrap_TemplatesCredoFromEnvelope(t *testing.T) {
	dir := t.TempDir()
	o := outfit.New(dir)
	// Loader-shape: system.credo is an envelope object whose body
	// lives in `content` (no frontmatter on the credo file).
	cb := snap(`{"system.credo":{"content":"hello {{.Provider}}","filePath":"/x"}}`)

	patch, err := o.Bootstrap(cb, outfit.BootCtx{Provider: "anthropic"})
	require.NoError(t, err)
	assert.Equal(t, `"hello anthropic"`, string(patch.Set["system.prompt"]))
}

func TestBootstrap_IdempotentWhenPromptSet(t *testing.T) {
	o := outfit.New(t.TempDir())
	cb := snap(`{"system.prompt":"already done","system.credo":"new body"}`)

	patch, err := o.Bootstrap(cb, outfit.BootCtx{})
	require.NoError(t, err)
	assert.True(t, patch.IsEmpty(), "Bootstrap is a no-op when system.prompt is set")
}

func TestBootstrap_DoesNotTouchSkills(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "go.md"),
		[]byte("---\nname: go\n---\nbody"), 0600))

	// Bootstrap should only produce system.prompt now; skills are the
	// loader's responsibility via the dirName form.
	patch, err := outfit.New(dir).Bootstrap(snap(`{"system.credo":"x"}`), outfit.BootCtx{})
	require.NoError(t, err)
	for k := range patch.Set {
		assert.NotContains(t, k, "skills", "Bootstrap must not touch skills keys; got %q", k)
	}
}

// snap builds a chalkboard.Snapshot from a JSON object literal.
func snap(jsonObj string) chalkboard.Snapshot {
	out := chalkboard.Snapshot{}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(jsonObj), &raw); err != nil {
		panic(err)
	}
	for k, v := range raw {
		out[k] = v
	}
	return out
}
