package figaro_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/tool"
)

// seedCredo writes system.credo onto a chalkboard so Outfitter.Bootstrap
// has something to template into system.prompt.
func seedCredo(t *testing.T, cb *chalkboard.State, body string) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.credo": b,
	}})
}

func TestBootstrap_FreshAria_EmitsStateOnlyTic(t *testing.T) {
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)
	seedCredo(t, cb, "you are figaro")

	a := figaro.NewAgent(figaro.Config{
		ID:         "boot-aria",
		SocketPath: dir + "/sock",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	t.Cleanup(func() { a.Kill() })

	// One state-only tic: Role=user, no Content, Patches set system.prompt.
	msgs := a.Context()
	require.Len(t, msgs, 1, "fresh aria must have exactly one bootstrap tic")
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Empty(t, msgs[0].Content, "bootstrap tic carries no Content")
	require.Len(t, msgs[0].Patches, 1)
	set := msgs[0].Patches[0].Set
	assert.Contains(t, set, "system.prompt")

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

	// First lifetime: bootstrap fires.
	cb, err := chalkboard.Open(cbPath)
	require.NoError(t, err)
	seedCredo(t, cb, "first prompt")
	a1 := figaro.NewAgent(figaro.Config{
		ID:         "boot-aria",
		SocketPath: dir + "/sock",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	require.Len(t, a1.Context(), 1)
	a1.Kill()

	// Second lifetime: re-open chalkboard from the saved file. system.prompt
	// is already set, so the second-phase outfit returns an empty patch
	// and no new tic is emitted.
	cb2, err := chalkboard.Open(cbPath)
	require.NoError(t, err)
	snap := cb2.Snapshot()
	require.Contains(t, snap, "system.prompt")

	a2 := figaro.NewAgent(figaro.Config{
		ID:         "boot-aria",
		SocketPath: dir + "/sock2",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb2,
	})
	t.Cleanup(func() { a2.Kill() })
	assert.Empty(t, a2.Context(),
		"restored aria with system.prompt already set must not emit a second bootstrap tic")
}
