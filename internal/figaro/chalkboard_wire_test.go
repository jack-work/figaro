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
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// chalkSpyProvider captures the IR messages EncodeMessage is called
// with so tests can inspect the per-message Patches the agent
// attached during catchUpTranslation.
type chalkSpyProvider struct {
	mu       sync.Mutex
	encoded  []message.Message
	sentRuns int
	cache    store.Log[[]json.RawMessage] // optional, set by tests that inspect cache state
}

func (p *chalkSpyProvider) Name() string                                           { return "spy" }
func (p *chalkSpyProvider) Fingerprint() string                                    { return "spy/v0" }
func (p *chalkSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *chalkSpyProvider) SetModel(string)                                        {}

// encode records every message it's asked to encode. Returns a stub
// payload so the cache lookup hits next turn.
func (p *chalkSpyProvider) encode(msg message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	p.mu.Lock()
	p.encoded = append(p.encoded, msg)
	p.mu.Unlock()
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}

func (p *chalkSpyProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sentRuns++
	p.mu.Unlock()
	mockCatchUp(in.FigLog, p.cache, p.encode, p.Fingerprint())
	mockPushAssistant(in.FigLog, p.cache, bus, p.encode, p.Fingerprint(), "ok")
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
	sub, unsub := subscribeChan(a)
	defer unsub()

	a.SubmitPrompt(rpc.QuaRequest{Text: text, Chalkboard: cb})

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
		ID:         "test-aria",
		SocketPath: dir + "/sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
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

func TestWire_Context_IsAdditive(t *testing.T) {
	// Context is purely additive: keys present in the snapshot but
	// absent from a subsequent Context are NOT removed. This lets
	// clients ship a partial view (just the keys they own — cwd,
	// datetime, env) without racing concurrent set/unset.
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
	assert.Empty(t, prov.lastTurnPatches(), "empty Context must not produce a removal patch")
}

func TestWire_Context_DoesNotRemoveUnmentionedSnapshotKeys(t *testing.T) {
	// A bootstrapped chalkboard may contain keys (skills, loadout
	// values, etc.) the client never carries in Context. Sending a
	// Context turn whose contents differ from those keys must not
	// remove them — only set the keys the client explicitly named.
	a, prov, cb := newAgentWithChalkboard(t)

	// Seed something the client does NOT carry in Context.
	cb.Apply(chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"skills.go": json.RawMessage(`{"description":"go body"}`),
		},
	})

	runOneTurn(t, a, "first", &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	})
	require.Equal(t, 1, prov.sendCount())
	patches := prov.lastTurnPatches()
	for _, p := range patches {
		assert.Empty(t, p.Remove, "Context must never emit Remove")
		_, hadSkills := p.Set["skills.go"]
		assert.False(t, hadSkills, "Context must not republish snapshot-only keys")
	}
	// Snapshot key survives.
	snap := cb.Snapshot()
	_, ok := snap["skills.go"]
	assert.True(t, ok, "skills.go must remain on the chalkboard")
}
