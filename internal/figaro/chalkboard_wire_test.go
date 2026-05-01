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

// chalkSpyProvider captures the IR Block Send is called with so tests
// can inspect the per-message Patches the agent attached.
type chalkSpyProvider struct {
	mu             sync.Mutex
	receivedBlocks []*message.Block
}

func (p *chalkSpyProvider) Name() string { return "spy" }
func (p *chalkSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *chalkSpyProvider) SetModel(string) {}
func (p *chalkSpyProvider) Send(ctx context.Context, block *message.Block, snapshot chalkboard.Snapshot, tools []provider.Tool, maxTokens int) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	// Deep-copy the block: the agent owns the underlying memstore and
	// will keep mutating it after Send returns.
	copyMsgs := make([]message.Message, len(block.Messages))
	copy(copyMsgs, block.Messages)
	p.receivedBlocks = append(p.receivedBlocks, &message.Block{
		Header:   block.Header,
		Messages: copyMsgs,
	})
	p.mu.Unlock()

	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		msg := &message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("ok")},
			StopReason: message.StopEnd,
			Usage:      &message.Usage{InputTokens: 1, OutputTokens: 1},
			Timestamp:  time.Now().UnixMilli(),
		}
		ch <- provider.StreamEvent{Delta: "ok", ContentType: message.ContentText, Message: msg}
		ch <- provider.StreamEvent{Done: true, Message: msg}
	}()
	return ch, nil
}

func (p *chalkSpyProvider) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.receivedBlocks)
}

// lastTurnPatches returns the patches attached to the leaf user-role
// Message of the most-recent Send. Empty if no user-role leaf or no
// patches.
func (p *chalkSpyProvider) lastTurnPatches() []message.Patch {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.receivedBlocks) == 0 {
		return nil
	}
	msgs := p.receivedBlocks[len(p.receivedBlocks)-1].Messages
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
