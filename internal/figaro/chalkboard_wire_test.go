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

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tool"
)

// chalkSpyProvider captures the IR messages Send is called with so
// tests can inspect the per-message Patches the agent attached.
type chalkSpyProvider struct {
	mu       sync.Mutex
	received [][]message.Message
}

func (p *chalkSpyProvider) Name() string                                          { return "spy" }
func (p *chalkSpyProvider) Fingerprint() string                                   { return "spy/v0" }
func (p *chalkSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *chalkSpyProvider) SetModel(string)                                       {}
func (p *chalkSpyProvider) Decode(raw []json.RawMessage) ([]message.Message, error) {
	return mockDecodeNative(raw)
}
// Encode captures the IR messages it was asked to project.
func (p *chalkSpyProvider) Encode(_ context.Context, msgs []message.Message, _ chalkboard.Snapshot, _ causal.Slice[message.ProviderTranslation], _ []provider.Tool, _ int) ([]byte, provider.ProjectionSummary, error) {
	p.mu.Lock()
	copyMsgs := make([]message.Message, len(msgs))
	copy(copyMsgs, msgs)
	p.received = append(p.received, copyMsgs)
	p.mu.Unlock()
	return nil, provider.ProjectionSummary{Fingerprint: p.Fingerprint()}, nil
}

func (p *chalkSpyProvider) Send(ctx context.Context, body []byte, bus provider.Bus) ([]json.RawMessage, error) {
	return []json.RawMessage{mockPushAssistant(bus, "ok")}, nil
}

func (p *chalkSpyProvider) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.received)
}

// lastTurnPatches returns the patches attached to the leaf user-role
// Message of the most-recent Send. Empty if no user-role leaf or no
// patches.
func (p *chalkSpyProvider) lastTurnPatches() []message.Patch {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.received) == 0 {
		return nil
	}
	msgs := p.received[len(p.received)-1]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.RoleUser {
			return msgs[i].Patches
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

	tmpls, err := chalkboard.LoadDefaultTemplates()
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
		ChalkboardTemplates: tmpls,
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
