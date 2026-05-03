package figaro_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tool"
)

// chalkSpyProvider captures the IR messages EncodeMessage is called
// with so tests can inspect the per-message Patches the agent
// attached during catchUpTranslation.
type chalkSpyProvider struct {
	mu       sync.Mutex
	encoded  []message.Message
	sentRuns int
}

func (p *chalkSpyProvider) Name() string                                          { return "spy" }
func (p *chalkSpyProvider) Fingerprint() string                                   { return "spy/v0" }
func (p *chalkSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *chalkSpyProvider) SetModel(string)                                       {}
func (p *chalkSpyProvider) Decode(payload []json.RawMessage) ([]message.Message, error) {
	return mockDecode(payload)
}

// Encode records every message it's asked to encode. Returns a stub
// payload so the cache lookup hits next turn.
func (p *chalkSpyProvider) Encode(msg message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	p.mu.Lock()
	p.encoded = append(p.encoded, msg)
	p.mu.Unlock()
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}

func (p *chalkSpyProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}

func (p *chalkSpyProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sentRuns++
	p.mu.Unlock()
	mockPushAssistant(bus, "ok")
	return nil
}

func (p *chalkSpyProvider) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sentRuns
}

// lastTurnPatches returns the patches attached to the most recent
// user-role message handed to EncodeMessage. Empty if none.
func (p *chalkSpyProvider) lastTurnPatches() []message.Patch {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.encoded) - 1; i >= 0; i-- {
		if p.encoded[i].Role == message.RoleUser {
			return p.encoded[i].Patches
		}
	}
	return nil
}

// runOneTurn submits a prompt with the given chalkboard input and waits
// for the turn to complete (via stream.done).
func runOneTurn(t *testing.T, a *figaro.Agent, text string, cb *rpc.ChalkboardInput) {
	t.Helper()
	sub := a.Subscribe()
	defer a.Unsubscribe(sub)

	a.SubmitPrompt(rpc.PromptRequest{Text: text, Chalkboard: cb})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-sub:
			if n.Method == rpc.MethodDone {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for stream.done")
		}
	}
}

// newAgentWithChalkboard builds an Agent wired with a per-aria
// *chalkboard.State and the embedded default templates.
func newAgentWithChalkboard(t *testing.T) (*figaro.Agent, *chalkSpyProvider, *chalkboard.State) {
	t.Helper()
	dir := t.TempDir()
	cb, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)

	prov := &chalkSpyProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID:                  "test-aria",
		SocketPath:          dir + "/sock",
		Provider:            prov,
		Model:               "claude-test",
		Cwd:                 "/tmp",
		Root:                "/tmp",
		MaxTokens:           1024,
		Tools:               tool.NewRegistry(),
		Chalkboard:          cb,
	})
	t.Cleanup(func() { a.Kill() })
	return a, prov, cb
}

// patchSets collects the set keys from a slice of patches.
func patchSets(ps []message.Patch) []string {
	seen := map[string]struct{}{}
	for _, p := range ps {
		for k := range p.Set {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// --- Wire-protocol coverage ---
//
// With log unification, the agent attaches one combined patch to the
// in-progress tic per user-prompt event. We assert patch presence and
// keys; rendering is the provider's job (covered in
// internal/provider/anthropic).

func TestWire_ContextOnly_DiffsAndApplies(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	cb1 := &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb1)
	require.Equal(t, 1, prov.sendCount())
	patches := prov.lastTurnPatches()
	require.Len(t, patches, 1, "first turn should attach 1 combined patch")
	assert.ElementsMatch(t, []string{"cwd"}, patchSets(patches))
}

func TestWire_ContextOnly_NoChange_NoPatch(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)
	cb := &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb)
	runOneTurn(t, a, "second", cb) // identical context
	require.Equal(t, 2, prov.sendCount())
	assert.Empty(t, prov.lastTurnPatches(), "identical context must produce 0 patches on a subsequent turn")
}

func TestWire_PatchOnly_AppliesDirectly(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	runOneTurn(t, a, "first", nil)
	require.Equal(t, 1, prov.sendCount())

	cb := &rpc.ChalkboardInput{
		Patch: &rpc.ChalkboardPatch{
			Set: map[string]json.RawMessage{
				"cwd": json.RawMessage(`"/home/beta"`),
			},
		},
	}
	runOneTurn(t, a, "second", cb)
	require.Equal(t, 2, prov.sendCount())
	patches := prov.lastTurnPatches()
	require.Len(t, patches, 1)
	assert.ElementsMatch(t, []string{"cwd"}, patchSets(patches))
}

func TestWire_ContextAndPatch_Combined(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	cb := &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
		Patch: &rpc.ChalkboardPatch{
			Set: map[string]json.RawMessage{
				"model": json.RawMessage(`"claude-opus"`),
			},
		},
	}
	runOneTurn(t, a, "first", cb)
	require.Equal(t, 1, prov.sendCount())
	patches := prov.lastTurnPatches()
	require.Len(t, patches, 1, "context + patch are merged into one combined patch")
	assert.ElementsMatch(t, []string{"cwd", "model"}, patchSets(patches))
}

func TestWire_NeitherContextNorPatch_NoOp(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	runOneTurn(t, a, "first", nil)
	require.Equal(t, 1, prov.sendCount())
	assert.Empty(t, prov.lastTurnPatches(), "no chalkboard input → no patches")
}

func TestWire_ContextRemoval(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	runOneTurn(t, a, "first", &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	})

	runOneTurn(t, a, "second", &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{},
	})
	require.Equal(t, 2, prov.sendCount())
	patches := prov.lastTurnPatches()
	require.Len(t, patches, 1, "removal must still attach a patch (the timeline records it)")
	require.Empty(t, patches[0].Set)
	assert.ElementsMatch(t, []string{"cwd"}, patches[0].Remove)
}
