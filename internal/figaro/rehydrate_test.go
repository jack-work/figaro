package figaro_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/tool"
)

// setCredo replaces system.credo on cb. The next Outfitter.Bootstrap
// run will re-template against this body.
func setCredo(t *testing.T, cb *chalkboard.State, body string) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.credo": b,
	}})
}

// promptOnceAndWait sends a prompt and waits for the full turn (the
// chalkSpyProvider always closes with "ok"), so the bootstrap patch
// has time to fold onto the first tic.
func promptOnceAndWait(t *testing.T, a *figaro.Agent, text string) {
	t.Helper()
	sub, unsub := subscribeChan(a)
	defer unsub()
	a.Prompt(text)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-sub:
			if n.Method == "stream.done" {
				return
			}
		case <-deadline:
			t.Fatal("turn never completed")
		}
	}
}

func TestRehydrate_DryRun_DoesNotMutateChalkboard(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)
	setCredo(t, cb, "version one")

	a := figaro.NewAgent(figaro.Config{
		ID:         "rehydrate-aria",
		SocketPath: dir + "/sock",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	t.Cleanup(func() { a.Kill() })

	// Drive a prompt to fire bootstrap and seed system.prompt.
	promptOnceAndWait(t, a, "first")

	// Mutate the source credo → next Rehydrate should detect a diff.
	setCredo(t, cb, "version two")
	set, removed, applied, err := a.Rehydrate(true /* dryRun */)
	require.NoError(t, err)
	assert.False(t, applied, "dry-run must not persist")
	assert.Contains(t, set, "system.prompt")
	assert.Empty(t, removed)

	// system.prompt is still the bootstrap-time render ("version one").
	snap := cb.Snapshot()
	require.Contains(t, snap, "system.prompt")
	assert.Contains(t, string(snap["system.prompt"]), "version one")
}

func TestRehydrate_Apply_EmitsStateOnlyTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)
	setCredo(t, cb, "version one")

	a := figaro.NewAgent(figaro.Config{
		ID:         "rehydrate-aria",
		SocketPath: dir + "/sock",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	t.Cleanup(func() { a.Kill() })

	promptOnceAndWait(t, a, "first")
	startCount := len(a.Context())

	setCredo(t, cb, "version two")
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
		if len(msgs) > startCount {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Greater(t, len(msgs), startCount, "rehydrate must append a state-only tic")
	tic := msgs[len(msgs)-1]
	assert.Equal(t, message.RoleUser, tic.Role)
	assert.Empty(t, tic.Content, "rehydrate tic carries only Patches")
	require.Len(t, tic.Patches, 1)
	assert.Contains(t, tic.Patches[0].Set, "system.prompt")

	// Chalkboard now reflects "version two".
	snap := cb.Snapshot()
	assert.Contains(t, string(snap["system.prompt"]), "version two")
}

func TestRehydrate_NoChanges_NoTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)
	setCredo(t, cb, "stable")

	a := figaro.NewAgent(figaro.Config{
		ID:         "rehydrate-aria",
		SocketPath: dir + "/sock",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	t.Cleanup(func() { a.Kill() })

	promptOnceAndWait(t, a, "first")
	startCount := len(a.Context())

	set, removed, applied, err := a.Rehydrate(false)
	require.NoError(t, err)
	assert.False(t, applied, "no diff → no tic")
	assert.Empty(t, set)
	assert.Empty(t, removed)

	// Give the actor a moment in case anything was queued anyway.
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, a.Context(), startCount,
		"no rehydrate tic must be appended when there's no diff")
}
