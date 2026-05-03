package figaro_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
)

func TestRehydrate_DryRun_DoesNotMutateChalkboard(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)

	scribe := &fakeScribe{prompt: "version one"}
	a := figaro.NewAgent(figaro.Config{
		ID:                  "rehydrate-aria",
		SocketPath:          dir + "/sock",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              scribe,
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
	})
	t.Cleanup(func() { a.Kill() })

	// Mutate the credo "on disk" → next Rehydrate should detect a diff.
	scribe.prompt = "version two"
	set, removed, applied, err := a.Rehydrate(true /* dryRun */)
	require.NoError(t, err)
	assert.False(t, applied, "dry-run must not persist")
	assert.Contains(t, set, "system.prompt")
	assert.Empty(t, removed)

	// Chalkboard.system.prompt should still be "version one".
	snap := cb.Snapshot()
	require.Contains(t, snap, "system.prompt")
	assert.Contains(t, string(snap["system.prompt"]), "version one")
}

func TestRehydrate_Apply_EmitsStateOnlyTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)

	scribe := &fakeScribe{prompt: "version one"}
	a := figaro.NewAgent(figaro.Config{
		ID:                  "rehydrate-aria",
		SocketPath:          dir + "/sock",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              scribe,
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
	})
	t.Cleanup(func() { a.Kill() })

	require.Len(t, a.Context(), 1, "bootstrap tic must already exist")

	scribe.prompt = "version two"
	set, removed, applied, err := a.Rehydrate(false)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.Contains(t, set, "system.prompt")
	assert.Empty(t, removed)

	// Wait for the actor to drain the rehydrate event.
	deadline := time.Now().Add(1 * time.Second)
	var msgs []message.Message
	for time.Now().Before(deadline) {
		msgs = a.Context()
		if len(msgs) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Len(t, msgs, 2, "rehydrate must append a state-only tic")
	assert.Equal(t, message.RoleUser, msgs[1].Role)
	assert.Empty(t, msgs[1].Content, "rehydrate tic carries only Patches")
	require.Len(t, msgs[1].Patches, 1)
	assert.Contains(t, msgs[1].Patches[0].Set, "system.prompt")

	// Chalkboard now reflects "version two".
	snap := cb.Snapshot()
	assert.Contains(t, string(snap["system.prompt"]), "version two")
}

func TestRehydrate_NoChanges_NoTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:                  "rehydrate-aria",
		SocketPath:          dir + "/sock",
		Provider:            &chalkSpyProvider{},
		Model:               "claude-test",
		Scribe:              &fakeScribe{prompt: "stable"},
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
	})
	t.Cleanup(func() { a.Kill() })

	require.Len(t, a.Context(), 1)

	set, removed, applied, err := a.Rehydrate(false)
	require.NoError(t, err)
	assert.False(t, applied, "no diff → no tic")
	assert.Empty(t, set)
	assert.Empty(t, removed)

	// Give the actor a moment in case anything was queued anyway.
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, a.Context(), 1, "no rehydrate tic must be appended when there's no diff")
}
