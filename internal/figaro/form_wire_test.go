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

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// formSpyProvider captures the IR messages EncodeMessage is called
// with so tests can inspect the per-message Patches the agent
// attached during catchUpTranslation.
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

// encode records every message it's asked to encode. Returns a stub
// payload so the cache lookup hits next turn.
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

// lastTurnPatches returns the patches attached to the most recent
// user-role message handed to EncodeMessage. Empty if none.
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

// runOneTurn submits a prompt with the given form input and waits
// for the turn to complete (via turn.done).
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

// newAgentWithForm builds an Agent wired with a per-aria
// *form.State and the embedded default templates.
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
	a, prov, _ := newAgentWithForm(t)

	// First turn always carries the bootstrap patch: burn it off
	// so the assertions test steady-state Context/Patch semantics.
	runOneTurn(t, a, "boot", nil)

	cb1 := &rpc.FormInput{
		Context: map[string]json.RawMessage{
			"cwd": json.RawMessage(`"/home/alpha"`),
		},
	}
	runOneTurn(t, a, "first", cb1)
	require.Equal(t, 2, prov.sendCount())
	// The patch lands in the FORM CHANNEL, not on the message: an aria is
	// always backed, so there is no inline-patch path any more.
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
		Patch: &rpc.FormPatch{
			Set: map[string]json.RawMessage{
				"cwd": json.RawMessage(`"/home/beta"`),
			},
		},
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
		Patch: &rpc.FormPatch{
			Set: map[string]json.RawMessage{
				// NOT "model": that key is harness-owned, and an unprivileged
				// write to it is refused -- which takes the whole patch down
				// with it. The ephemeral path used to hide that by stapling the
				// patch to the message instead of applying it.
				"note": json.RawMessage(`"combined"`),
			},
		},
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
	// Context is purely additive: keys present in the snapshot but
	// absent from a subsequent Context are NOT removed. This lets
	// clients ship a partial view (just the keys they own: cwd,
	// datetime, env) without racing concurrent set/unset.
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
	// A loaded form may contain keys (skills, outfit
	// values, etc.) the client never carries in Context. Sending a
	// Context turn whose contents differ from those keys must not
	// remove them: only set the keys the client explicitly named.
	a, prov, cb := newAgentWithForm(t)

	// Seed something the client does NOT carry in Context.
	cb.Apply(form.Patch{
		Set: map[string]json.RawMessage{
			"skills.go": json.RawMessage(`{"description":"go body"}`),
		},
	})

	runOneTurn(t, a, "first", &rpc.FormInput{
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
	ok := snap.Has("skills.go")
	assert.True(t, ok, "skills.go must remain on the form")
}

// A conditional set is the guard a read-modify-write needs: `fig set x[0]` reads
// the value, edits it in the client, and writes the whole key back, so a second
// shell doing the same thing must not silently win. The check happens in the
// form's writer, atomically with the append: checking at accept would answer
// about a version the patch never met.
func TestSetRefusesAStaleVersion(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: id, Provider: &formSpyProvider{},
		Backend: b, Tools: tool.NewRegistry(), Form: mustForm(t),
	})
	defer a.Kill()

	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{"a": json.RawMessage(`1`)}}, 0)
	require.NoError(t, err)
	var read uint64
	require.Eventually(t, func() bool {
		read = a.Version()
		v, ok := a.Snapshot().Get("a")
		return ok && string(v) == `1` && read > 0
	}, time.Second, 5*time.Millisecond)

	// Someone else writes in between: `read` is stale by the time this lands.
	_, _, err = a.Set(form.Patch{Set: map[string]json.RawMessage{"b": json.RawMessage(`2`)}}, 0)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.Version() > read }, time.Second, 5*time.Millisecond)
	moved := a.Version()

	_, _, err = a.Set(form.Patch{Set: map[string]json.RawMessage{"a": json.RawMessage(`3`)}}, read)
	require.NoError(t, err, "the refusal happens at the writer, not at accept")
	time.Sleep(50 * time.Millisecond)
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
