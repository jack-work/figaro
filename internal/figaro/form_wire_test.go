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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// formSpyProvider captures the IR messages EncodeMessage is called with.
type formSpyProvider struct {
	mu       sync.Mutex
	encoded  []message.Message
	sentRuns int
	cache    store.Log[[]json.RawMessage] // optional, set by tests that inspect cache state
}

func (p *formSpyProvider) Name() string                                           { return "spy" }
func (p *formSpyProvider) Fingerprint() string                                    { return "spy/v0" }
func (p *formSpyProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *formSpyProvider) SetModel(string)                                        {}

// encode returns a stub payload so the cache lookup hits next turn.
func (p *formSpyProvider) encode(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
	p.mu.Lock()
	p.encoded = append(p.encoded, msg)
	p.mu.Unlock()
	return []json.RawMessage{json.RawMessage(`{"role": livedoc.RoleInput,"content":[]}`)}, nil
}

func (p *formSpyProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sentRuns++
	p.mu.Unlock()
	mockCatchUp(in.FigLog, p.cache, p.encode, p.Fingerprint())
	mockPushAssistant(in.FigLog, p.cache, bus, p.encode, p.Fingerprint(), "ok")
	return nil
}

func (p *formSpyProvider) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sentRuns
}

// lastTurnPatches is the patches on the most recent user-role message encoded.
func (p *formSpyProvider) lastTurnPatches() []message.Patch {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.encoded) - 1; i >= 0; i-- {
		if p.encoded[i].Role == message.RoleInput {
			return p.encoded[i].Patches
		}
	}
	return nil
}

func runOneTurn(t *testing.T, a *figaro.Agent, text string, cb *rpc.FormInput) {
	t.Helper()
	sub, unsub := subscribeChan(a)
	defer unsub()

	a.SubmitPrompt(rpc.QuaRequest{Text: text, Form: cb})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-sub:
			if n.Method == rpc.MethodTurnDone {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn.done")
		}
	}
}

func newAgentWithForm(t *testing.T) (*figaro.Agent, *formSpyProvider, *form.State) {
	t.Helper()
	dir := t.TempDir()
	cb, err := form.Open(filepath.Join(dir, "the form channel"))
	require.NoError(t, err)

	prov := &formSpyProvider{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: dir + "/sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(func() { a.Kill() })
	return a, prov, cb
}

func TestWire_ContextOnly_DiffsAndApplies(t *testing.T) {
	a, prov, _ := newAgentWithForm(t)

	// Burn off the bootstrap patch the first turn carries.
	runOneTurn(t, a, "boot", nil)

	cb1 := &rpc.FormInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb1)
	require.Equal(t, 2, prov.sendCount())
	cwd, _ := a.Snapshot().Get("cwd")
	assert.Equal(t, `"/home/alpha"`, string(cwd))
}

func TestWire_ContextOnly_NoChange_NoPatch(t *testing.T) {
	a, prov, _ := newAgentWithForm(t)
	cb := &rpc.FormInput{
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
	a, prov, _ := newAgentWithForm(t)

	runOneTurn(t, a, "first", nil)
	require.Equal(t, 1, prov.sendCount())

	cb := &rpc.FormInput{
		Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/beta"`),
		}, nil)),
	}
	runOneTurn(t, a, "second", cb)
	require.Equal(t, 2, prov.sendCount())
	cwd, _ := a.Snapshot().Get("cwd")
	assert.Equal(t, `"/home/beta"`, string(cwd))
}

func TestWire_ContextAndPatch_Combined(t *testing.T) {
	a, prov, _ := newAgentWithForm(t)
	runOneTurn(t, a, "boot", nil) // burn off bootstrap turn

	cb := &rpc.FormInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
		Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			// Not "model": a harness-owned key refuses the whole patch.
			"note": json.RawMessage(`"combined"`),
		}, nil)),
	}
	runOneTurn(t, a, "first", cb)
	require.Equal(t, 2, prov.sendCount())
	snap := a.Snapshot()
	cwd, _ := snap.Get("cwd")
	note, _ := snap.Get("note")
	assert.Equal(t, `"/home/alpha"`, string(cwd), "context and patch must merge into ONE applied write")
	assert.Equal(t, `"combined"`, string(note))
}

func TestWire_NeitherContextNorPatch_NoOp(t *testing.T) {
	a, prov, _ := newAgentWithForm(t)
	runOneTurn(t, a, "boot", nil) // burn off bootstrap turn

	runOneTurn(t, a, "first", nil)
	require.Equal(t, 2, prov.sendCount())
	assert.Empty(t, prov.lastTurnPatches(), "no form input → no patches")
}

func TestWire_Context_IsAdditive(t *testing.T) {
	// Context is additive: a partial view must not race concurrent set/unset.
	a, prov, _ := newAgentWithForm(t)

	runOneTurn(t, a, "first", &rpc.FormInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	})

	runOneTurn(t, a, "second", &rpc.FormInput{
		Context: map[string]json.RawMessage{},
	})
	require.Equal(t, 2, prov.sendCount())
	assert.Empty(t, prov.lastTurnPatches(), "empty Context must not produce a removal patch")
}

func TestWire_Context_DoesNotRemoveUnmentionedSnapshotKeys(t *testing.T) {
	a, prov, cb := newAgentWithForm(t)

	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.go": json.RawMessage(`{"description":"go body"}`),
	}, nil))

	runOneTurn(t, a, "first", &rpc.FormInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	})
	require.Equal(t, 1, prov.sendCount())
	patches := prov.lastTurnPatches()
	for _, p := range patches {
		for _, e := range p.Entries() {
			assert.False(t, e.IsRemoval(), "Context must never emit Remove")
		}
		_, hadSkills := p.Entry("skills.go")
		assert.False(t, hadSkills, "Context must not republish snapshot-only keys")
	}
	snap := cb.Snapshot()
	ok := snap.Has("skills.go")
	assert.True(t, ok, "skills.go must remain on the form")
}

// A conditional set is checked in the form's writer, atomically with the append.
func TestSetRefusesAStaleVersion(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: id, Provider: &formSpyProvider{},
		Backend: b, Tools: tool.NewRegistry(), Form: mustForm(t),
	})
	defer a.Kill()

	_, _, err := a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{"a": json.RawMessage(`1`)}, nil), 0)
	require.NoError(t, err)
	var read uint64
	require.Eventually(t, func() bool {
		read = a.Version()
		v, ok := a.Snapshot().Get("a")
		return ok && string(v) == `1` && read > 0
	}, time.Second, 5*time.Millisecond)

	_, _, err = a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{"b": json.RawMessage(`2`)}, nil), 0)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.Version() > read }, time.Second, 5*time.Millisecond)
	moved := a.Version()

	// The refusal reaches the caller: the form's actor applies before this returns.
	_, _, err = a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{"a": json.RawMessage(`3`)}, nil), read)
	require.Error(t, err, "a stale conditional set must be refused to the caller")
	v, _ := a.Snapshot().Get("a")
	assert.Equal(t, `1`, string(v), "a stale conditional set must not land")
	assert.Equal(t, moved, a.Version(), "and must not advance the form")
}

func mustForm(t *testing.T) *form.State {
	t.Helper()
	st, err := form.Open("")
	require.NoError(t, err)
	return st
}

// A refused key reaches the caller on the reply it is waiting for.
func TestRefusedFormInputIsReportedToTheCaller(t *testing.T) {
	a, _, _ := newAgentWithForm(t)

	err := a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{
		Text: "set a key that is not mine to set",
		Form: &rpc.FormInput{Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"model": json.RawMessage(`"claude-opus"`),
		}, nil))},
	}, "")
	require.Error(t, err, "a harness-owned key must be refused to the caller's face")
	require.Contains(t, err.Error(), "model")

	_, ok := a.Snapshot().Get("model")
	require.False(t, ok)
}

func ptrPatch(p form.Patch) *form.Patch { return &p }
