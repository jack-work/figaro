package figaro_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
)

// fakeScribe returns a fixed prompt — the bootstrap path doesn't need a
// real credo template on disk.
type fakeScribe struct{ prompt string }

func (f *fakeScribe) Build(credo.Context) (string, error) { return f.prompt, nil }

func TestBootstrap_FreshAria_EmitsStateOnlyTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)

	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:                  "boot-aria",
		SocketPath:          dir + "/sock",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              &fakeScribe{prompt: "you are figaro"},
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
		ChalkboardTemplates: tmpls,
	})
	t.Cleanup(func() { a.Kill() })

	// One state-only tic: Role=user, no Content, Patches set system.*
	msgs := a.Context()
	require.Len(t, msgs, 1, "fresh aria must have exactly one bootstrap tic")
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Empty(t, msgs[0].Content, "bootstrap tic carries no Content")
	require.Len(t, msgs[0].Patches, 1)
	set := msgs[0].Patches[0].Set
	assert.Contains(t, set, "system.prompt")
	assert.Contains(t, set, "system.model")
	assert.Contains(t, set, "system.provider")

	var sp string
	require.NoError(t, json.Unmarshal(set["system.prompt"], &sp))
	assert.Equal(t, "you are figaro", sp)

	// Chalkboard snapshot should reflect the patch.
	snap := cb.Snapshot()
	assert.Contains(t, snap, "system.prompt")
}

func TestBootstrap_RestoredAria_SkipsBootstrap(t *testing.T) {
	dir := t.TempDir()
	cbPath := filepath.Join(dir, "chalkboard.json")

	// First lifetime: create an agent so the bootstrap fires.
	cb, err := chalkboard.Open(cbPath)
	require.NoError(t, err)
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	a1 := figaro.NewAgent(figaro.Config{
		ID:                  "boot-aria",
		SocketPath:          dir + "/sock",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              &fakeScribe{prompt: "first prompt"},
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
		ChalkboardTemplates: tmpls,
	})
	require.Len(t, a1.Context(), 1)
	a1.Kill()

	// Second lifetime: re-open chalkboard from the saved file. The
	// system.prompt key already exists, so no new bootstrap tic.
	cb2, err := chalkboard.Open(cbPath)
	require.NoError(t, err)
	snap := cb2.Snapshot()
	require.Contains(t, snap, "system.prompt")

	// Note: a2 has no Backend so memStore starts empty (the bootstrap
	// tic from a1 was never persisted to disk). What we're testing is
	// the chalkboard short-circuit: bootstrap doesn't double-run when
	// system.prompt is already set, regardless of memStore state.
	a2 := figaro.NewAgent(figaro.Config{
		ID:                  "boot-aria",
		SocketPath:          dir + "/sock2",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              &fakeScribe{prompt: "second prompt"},
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb2,
		ChalkboardTemplates: tmpls,
	})
	t.Cleanup(func() { a2.Kill() })
	assert.Empty(t, a2.Context(),
		"restored aria with system.prompt already set must not emit a second bootstrap tic")
}
