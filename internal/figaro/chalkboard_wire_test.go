package figaro_test

import (
	"context"
	"encoding/json"
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

// chalkSpyProvider captures the reminders Send is called with so tests
// can assert what the agent passed to the provider.
type chalkSpyProvider struct {
	mu             sync.Mutex
	receivedRems   [][]chalkboard.RenderedEntry
	receivedBlocks []*message.Block
}

func (p *chalkSpyProvider) Name() string                  { return "spy" }
func (p *chalkSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *chalkSpyProvider) SetModel(string)               {}
func (p *chalkSpyProvider) Send(ctx context.Context, block *message.Block, tools []provider.Tool, reminders []chalkboard.RenderedEntry, maxTokens int) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.receivedRems = append(p.receivedRems, append([]chalkboard.RenderedEntry(nil), reminders...))
	p.receivedBlocks = append(p.receivedBlocks, block)
	p.mu.Unlock()

	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		// Emit a trivial assistant reply so the turn ends cleanly.
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

func (p *chalkSpyProvider) lastReminders() []chalkboard.RenderedEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.receivedRems) == 0 {
		return nil
	}
	return p.receivedRems[len(p.receivedRems)-1]
}

func (p *chalkSpyProvider) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.receivedRems)
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

// newAgentWithChalkboard builds an Agent wired with an in-memory
// chalkboard.Store and the embedded default templates.
func newAgentWithChalkboard(t *testing.T) (*figaro.Agent, *chalkSpyProvider, chalkboard.Store) {
	t.Helper()
	dir := t.TempDir()
	cb, err := chalkboard.NewFileStore(dir)
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

// --- Wire-protocol coverage ---

func TestWire_ContextOnly_DiffsAndApplies(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	// Turn 1: send a context with cwd. Snapshot was empty → patch sets cwd.
	cb1 := &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb1)
	require.Equal(t, 1, prov.sendCount())
	rems := prov.lastReminders()
	require.Len(t, rems, 1, "first turn should produce 1 reminder for the new key")
	assert.Equal(t, "cwd", rems[0].Key)
}

func TestWire_ContextOnly_NoChange_NoReminders(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)
	cb := &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb)
	runOneTurn(t, a, "second", cb) // identical context
	require.Equal(t, 2, prov.sendCount())
	assert.Empty(t, prov.lastReminders(), "identical context on a subsequent turn must produce 0 reminders")
}

func TestWire_PatchOnly_AppliesDirectly(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	// Turn 1: empty context establishes a baseline.
	runOneTurn(t, a, "first", nil)
	require.Equal(t, 1, prov.sendCount())

	// Turn 2: explicit patch (no context). Should apply directly.
	cb := &rpc.ChalkboardInput{
		Patch: &rpc.ChalkboardPatch{
			Set: map[string]json.RawMessage{
				"cwd": json.RawMessage(`"/home/beta"`),
			},
		},
	}
	runOneTurn(t, a, "second", cb)
	require.Equal(t, 2, prov.sendCount())
	rems := prov.lastReminders()
	require.Len(t, rems, 1, "patch-only path should set the key and render a reminder")
	assert.Equal(t, "cwd", rems[0].Key)
	assert.Contains(t, rems[0].Body, "/home/beta")
}

func TestWire_ContextAndPatch_Combined(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	// Send context (cwd) + patch (model). Both should fire reminders.
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
	rems := prov.lastReminders()
	require.Len(t, rems, 2, "context + patch should produce 2 reminders (cwd from diff, model from patch)")

	keys := []string{rems[0].Key, rems[1].Key}
	assert.ElementsMatch(t, []string{"cwd", "model"}, keys)
}

func TestWire_NeitherContextNorPatch_NoOp(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	runOneTurn(t, a, "first", nil)
	require.Equal(t, 1, prov.sendCount())
	assert.Empty(t, prov.lastReminders(), "no chalkboard input → no reminders")
}

func TestWire_ContextRemoval(t *testing.T) {
	a, prov, _ := newAgentWithChalkboard(t)

	// Turn 1: set cwd.
	runOneTurn(t, a, "first", &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	})

	// Turn 2: send empty context. Server diffs vs persisted → cwd
	// should be removed. (Removed keys don't render — but they DO get
	// persisted to the chalkboard log, which we can't inspect from
	// here without coupling the test to internals. The contract: no
	// reminder, no error.)
	runOneTurn(t, a, "second", &rpc.ChalkboardInput{
		Context: map[string]json.RawMessage{},
	})
	require.Equal(t, 2, prov.sendCount())
	assert.Empty(t, prov.lastReminders(), "removal should not render a reminder (no template binding for removal)")
}
