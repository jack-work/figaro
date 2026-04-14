package credo_test

import (
	"os"
	"path/filepath"
	"strings"
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
	// Use the testdata directory as the config dir.
	scribe := credo.NewDefaultScribe("testdata")

	ctx := credo.Context{
		DateTime: "Thursday, March 20, 2026, 10PM EDT",
		Cwd:      "/home/gluck/dev/figaro",
		Root:     "/home/gluck/dev/figaro",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		FigaroID: "abc123",
	}

	prompt, err := scribe.Build(ctx)
	require.NoError(t, err)

	// Template fields should be resolved.
	assert.Contains(t, prompt, "Thursday, March 20, 2026, 10PM EDT")
	assert.Contains(t, prompt, "/home/gluck/dev/figaro")
	assert.Contains(t, prompt, "anthropic")
	assert.Contains(t, prompt, "claude-sonnet-4-20250514")
	assert.Contains(t, prompt, "abc123")

	// Skills should be appended.
	assert.Contains(t, prompt, "# Available Skills")
	assert.Contains(t, prompt, "websearch")
	assert.Contains(t, prompt, "pishot")
}

func TestDefaultScribe_Caching(t *testing.T) {
	scribe := credo.NewDefaultScribe("testdata")

	ctx := credo.Context{
		DateTime: "Thursday, March 20, 2026, 10PM EDT",
		Cwd:      "/tmp",
		Root:     "/tmp",
		Provider: "mock",
		Model:    "mock-model",
		FigaroID: "test",
	}

	prompt1, err := scribe.Build(ctx)
	require.NoError(t, err)

	prompt2, err := scribe.Build(ctx)
	require.NoError(t, err)

	// Same context + same file → should return cached result.
	assert.Equal(t, prompt1, prompt2)
}

func TestDefaultScribe_RebuildOnContextChange(t *testing.T) {
	scribe := credo.NewDefaultScribe("testdata")

	ctx1 := credo.Context{
		DateTime: "Thursday, March 20, 2026, 10PM EDT",
		Cwd:      "/tmp/a",
		Root:     "/tmp/a",
		Provider: "mock",
		Model:    "model-a",
		FigaroID: "test",
	}

	ctx2 := credo.Context{
		DateTime: "Thursday, March 20, 2026, 11PM EDT",
		Cwd:      "/tmp/b",
		Root:     "/tmp/b",
		Provider: "mock",
		Model:    "model-b",
		FigaroID: "test",
	}

	prompt1, err := scribe.Build(ctx1)
	require.NoError(t, err)

	prompt2, err := scribe.Build(ctx2)
	require.NoError(t, err)

	assert.NotEqual(t, prompt1, prompt2)
	assert.Contains(t, prompt1, "model-a")
	assert.Contains(t, prompt2, "model-b")
}

func TestDefaultScribe_MissingCredoFile(t *testing.T) {
	scribe := credo.NewDefaultScribe(t.TempDir())

	ctx := credo.Context{DateTime: "now", Cwd: "/tmp", Root: "/tmp"}
	_, err := scribe.Build(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "credo.md")
}

// --- CurrentContext ---

func TestCurrentContext(t *testing.T) {
	ctx := credo.CurrentContext("/work", "/project", "anthropic", "claude-sonnet-4", "fig-001", "bash, read, write, edit")

	assert.NotEmpty(t, ctx.DateTime)
	assert.Equal(t, "/work", ctx.Cwd)
	assert.Equal(t, "/project", ctx.Root)
	assert.Equal(t, "anthropic", ctx.Provider)
	assert.Equal(t, "claude-sonnet-4", ctx.Model)
	assert.Equal(t, "fig-001", ctx.FigaroID)
	assert.Equal(t, "bash, read, write, edit", ctx.Tools)
	assert.NotEmpty(t, ctx.Version)

	// DateTime should be hour precision (no minutes/seconds).
	assert.False(t, strings.Contains(ctx.DateTime, ":"), "DateTime should not contain minutes/seconds")
}
