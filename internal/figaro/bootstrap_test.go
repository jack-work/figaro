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

// waitForFirstUserTic blocks until the agent's figLog has at least
// one user-role message (i.e. the first prompt has been finalized).
func waitForFirstUserTic(t *testing.T, a *figaro.Agent) message.Message {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		msgs := a.Context()
		if len(msgs) > 0 && msgs[0].Role == message.RoleUser {
			return msgs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("first user tic never landed")
	return message.Message{}
}

func TestBootstrap_FirstPromptCarriesPatch(t *testing.T) {
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

	// Fresh aria — figLog is empty until the first prompt.
	require.Empty(t, a.Context(), "no bootstrap tic on construction")

	a.Prompt("hello")
	tic := waitForFirstUserTic(t, a)
	require.NotEmpty(t, tic.Content, "first tic carries the user text")
	require.Len(t, tic.Patches, 1, "first tic carries the bootstrap patch")
	set := tic.Patches[0].Set
	assert.Contains(t, set, "system.prompt")

	var sp string
	require.NoError(t, json.Unmarshal(set["system.prompt"], &sp))
	assert.Equal(t, "you are figaro", sp)

	// Chalkboard reflects the applied patch.
	assert.Contains(t, cb.Snapshot(), "system.prompt")
}

func TestBootstrap_RestoredAriaSkipsBootstrap(t *testing.T) {
	dir := t.TempDir()
	cbPath := filepath.Join(dir, "chalkboard.json")

	// First lifetime: prompt fires the bootstrap.
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
	a1.Prompt("hello")
	waitForFirstUserTic(t, a1)
	a1.Kill()

	// Second lifetime: chalkboard already has system.prompt. Bootstrap
	// returns an empty patch — the next first-prompt tic carries no
	// patches.
	cb2, err := chalkboard.Open(cbPath)
	require.NoError(t, err)
	require.Contains(t, cb2.Snapshot(), "system.prompt")

	a2 := figaro.NewAgent(figaro.Config{
		ID:         "boot-aria",
		SocketPath: dir + "/sock2",
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(dir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb2,
	})
	t.Cleanup(func() { a2.Kill() })

	a2.Prompt("hello again")
	tic := waitForFirstUserTic(t, a2)
	assert.Empty(t, tic.Patches, "restored aria's first prompt carries no bootstrap patch")
}
