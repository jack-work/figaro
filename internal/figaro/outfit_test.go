package figaro_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// writeOutfit drops an outfit TOML at configDir/outfits/<name>.toml.
func writeOutfit(t *testing.T, configDir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "outfits", name+".toml"), []byte(body), 0600))
}

// agentForOutfit builds an Agent with an Outfitter rooted at
// configDir and a form seeded by initial.
func agentForOutfit(t *testing.T, configDir string, initial form.Patch) *figaro.Agent {
	t.Helper()
	cb, err := form.Open(filepath.Join(t.TempDir(), "the form channel"))
	require.NoError(t, err)
	if !initial.IsEmpty() {
		cb.Apply(initial)
	}
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         "outfit-test",
		SocketPath: filepath.Join(t.TempDir(), "sock"),
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(configDir),
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(func() { a.Kill() })
	return a
}

// A layer that does not exist is refused where the call is accepted, with the
// closure attached — not logged at drain, where nobody sees it.
func TestSetRefusesAMissingLayer(t *testing.T) {
	dir := t.TempDir()
	a := agentForOutfit(t, dir, form.Patch{})

	_, _, err := a.Set(dressPatch("nope"), 0)
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)
}

// The same, one call further out: a prompt carrying a bad dressing fails the
// qua rather than queueing and losing the outfit at drain.
func TestPromptRefusedAtAccept(t *testing.T) {
	dir := t.TempDir()
	a := agentForOutfit(t, dir, form.Patch{})

	patch := dressPatch("nope")
	_, err := a.Handle(t.Context(), rpc.MethodQua, mustJSON(t, rpc.QuaRequest{
		Text: "hello",
		Form: &rpc.FormInput{Patch: &patch},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

// A named layer's keys land on the board, under whatever the same patch set
// itself: a client that wrote both meant its own value.
func TestSetMaterializesLayersUnderItsOwnKeys(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"base-model\"\ntone = \"dry\"\n")
	a := agentForOutfit(t, dir, form.Patch{})

	patch := dressPatch("base")
	patch.Set["system.model"] = json.RawMessage(`"mine"`)
	set, _, err := a.Set(patch, 0)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"system.model", "system.tone"}, set)

	require.Eventually(t, func() bool {
		snap := a.Snapshot()
		model, _ := snap.Get("system.model")
		tone, _ := snap.Get("system.tone")
		return string(model) == `"mine"` && string(tone) == `"dry"`
	}, time.Second, 5*time.Millisecond)
	assert.False(t, a.Snapshot().Has("layers"), "the directive must not reach the board")
}

// dressPatch is `-O <names>`: the layers directive a client sends.
func dressPatch(names ...string) form.Patch {
	b, _ := json.Marshal(names)
	return form.Patch{Set: map[string]json.RawMessage{"layers": b}}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
