package credo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/credo"
)

// --- Frontmatter parsing ---

func TestLoadSkills(t *testing.T) {
	skills, err := credo.LoadSkills("testdata/skills")
	require.NoError(t, err)
	require.Len(t, skills, 2)

	// Sort-independent check.
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	assert.True(t, names["websearch"])
	assert.True(t, names["pishot"])

	// Check a specific skill.
	var ws credo.Skill
	for _, s := range skills {
		if s.Name == "websearch" {
			ws = s
			break
		}
	}
	assert.Equal(t, "Search the web using the Brave Search API.", ws.Description)
	assert.Contains(t, ws.Content, "hush brave")
	assert.Contains(t, ws.FilePath, "websearch.md")
}

func TestLoadSkills_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	skills, err := credo.LoadSkills(dir)
	require.NoError(t, err)
	assert.Empty(t, skills)
}

func TestLoadSkills_NoDir(t *testing.T) {
	skills, err := credo.LoadSkills("/nonexistent/path")
	require.NoError(t, err)
	assert.Nil(t, skills)
}

func TestLoadSkills_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plain.md"), []byte("Just content, no frontmatter."), 0644)

	skills, err := credo.LoadSkills(dir)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "plain", skills[0].Name) // fallback to filename
	assert.Equal(t, "Just content, no frontmatter.", skills[0].Content)
}

// --- FormatSkills ---

func TestFormatSkills(t *testing.T) {
	skills := []credo.Skill{
		{Name: "websearch", Description: "Search the web.", FilePath: "/config/skills/websearch.md"},
		{Name: "pishot", Description: "Screenshots.", FilePath: "/config/skills/pishot.md"},
	}
	formatted := credo.FormatSkills(skills)
	assert.Contains(t, formatted, "# Available Skills")
	assert.Contains(t, formatted, "## websearch")
	assert.Contains(t, formatted, "*Search the web.*")
	assert.Contains(t, formatted, "## pishot")
}

// --- DefaultScribe ---

func TestDefaultScribe_Build(t *testing.T) {
	scribe := credo.NewDefaultScribe("testdata")

	ctx := credo.Context{
		Provider: "anthropic",
		FigaroID: "abc123",
		Version:  "v1",
	}

	prompt, err := scribe.Build(ctx)
	require.NoError(t, err)

	assert.Contains(t, prompt, "anthropic")
	assert.Contains(t, prompt, "abc123")
	assert.Contains(t, prompt, "v1")

	// Skills should be appended.
	assert.Contains(t, prompt, "# Available Skills")
	assert.Contains(t, prompt, "websearch")
	assert.Contains(t, prompt, "pishot")
}

func TestDefaultScribe_Caching(t *testing.T) {
	scribe := credo.NewDefaultScribe("testdata")

	ctx := credo.Context{
		Provider: "mock",
		FigaroID: "test",
		Version:  "v1",
	}

	prompt1, err := scribe.Build(ctx)
	require.NoError(t, err)

	prompt2, err := scribe.Build(ctx)
	require.NoError(t, err)

	// Same context + same file → cache hit.
	assert.Equal(t, prompt1, prompt2)
}

func TestDefaultScribe_RebuildOnContextChange(t *testing.T) {
	scribe := credo.NewDefaultScribe("testdata")

	ctx1 := credo.Context{Provider: "a", FigaroID: "test", Version: "v1"}
	ctx2 := credo.Context{Provider: "b", FigaroID: "test", Version: "v1"}

	prompt1, err := scribe.Build(ctx1)
	require.NoError(t, err)

	prompt2, err := scribe.Build(ctx2)
	require.NoError(t, err)

	assert.NotEqual(t, prompt1, prompt2)
	assert.Contains(t, prompt1, "Provider: a")
	assert.Contains(t, prompt2, "Provider: b")
}

func TestDefaultScribe_MissingCredoFile(t *testing.T) {
	scribe := credo.NewDefaultScribe(t.TempDir())

	ctx := credo.Context{Provider: "p", FigaroID: "f"}
	_, err := scribe.Build(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "credo.md")
}

// --- CurrentContext ---

func TestCurrentContext(t *testing.T) {
	ctx := credo.CurrentContext("anthropic", "fig-001")

	assert.Equal(t, "anthropic", ctx.Provider)
	assert.Equal(t, "fig-001", ctx.FigaroID)
	assert.NotEmpty(t, ctx.Version)
}
