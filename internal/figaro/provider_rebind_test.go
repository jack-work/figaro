package figaro_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
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
	"github.com/jack-work/figaro/internal/uiir"
)

// namedProvider is a one-round provider that records every Send it serves.
// Two of them under one factory is all it takes to observe which instance a
// turn actually reached.
type namedProvider struct {
	name  string
	sdk   bool
	sends atomic.Int32

	mu     sync.Mutex
	models []string
}

func (p *namedProvider) Name() string        { return p.name }
func (p *namedProvider) Fingerprint() string { return p.name + "/v0" }
func (p *namedProvider) SetModel(string)     {}
func (p *namedProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *namedProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	p.sends.Add(1)
	p.mu.Lock()
	if v := in.Snapshot.Lookup("system.model"); v != nil {
		p.models = append(p.models, *v)
	}
	p.mu.Unlock()
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("hello from " + p.name)},
		StopReason: message.StopEnd,
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

// rebindFactory hands out one namedProvider per (name, sdk-knob) pair and
// records the build requests, so a test can tell a rebuild from a reuse.
type rebindFactory struct {
	mu    sync.Mutex
	built []string // "name/sdk" per call, in order
	insts map[string]*namedProvider
	fail  map[string]bool
}

func newRebindFactory() *rebindFactory {
	return &rebindFactory{insts: map[string]*namedProvider{}, fail: map[string]bool{}}
}

func (f *rebindFactory) key(name string, knobs provider.Knobs) string {
	return fmt.Sprintf("%s/%t", name, knobs.UseOfficialSDK)
}

func (f *rebindFactory) build(name string, knobs provider.Knobs) (provider.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(name, knobs)
	f.built = append(f.built, key)
	if f.fail[name] {
		return nil, fmt.Errorf("no credentials for %s", name)
	}
	p, ok := f.insts[key]
	if !ok {
		p = &namedProvider{name: name, sdk: knobs.UseOfficialSDK}
		f.insts[key] = p
	}
	return p, nil
}

func (f *rebindFactory) inst(name string, sdk bool) *namedProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insts[fmt.Sprintf("%s/%t", name, sdk)]
}

func (f *rebindFactory) builds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.built...)
}

func (f *rebindFactory) setFail(name string, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[name] = fail
}

// runTurn submits a prompt, waits for turn.done, and returns its reason -
// which is where a failed round reports itself.
func runTurn(t *testing.T, a *figaro.Agent, ch <-chan rpc.Notification, text string) string {
	t.Helper()
	submitPrompt(a, text)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method != rpc.MethodTurnDone {
				continue
			}
			raw, err := json.Marshal(n.Params)
			require.NoError(t, err)
			var done rpc.DoneEntry
			require.NoError(t, json.Unmarshal(raw, &done))
			return done.Reason
		case <-timeout:
			t.Fatalf("timeout waiting for turn.done after %q", text)
		}
	}
}

func rebindAgent(t *testing.T, f *rebindFactory, boot map[string]json.RawMessage) (*figaro.Agent, <-chan rpc.Notification) {
	t.Helper()
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: boot})
	name := ""
	if raw, ok := cb.Snapshot().Get("system.provider"); ok {
		_ = json.Unmarshal(raw, &name)
	}
	prov, err := f.build(name, provider.Knobs{})
	require.NoError(t, err)
	a := figaro.NewAgent(figaro.Config{
		Projector:       uiir.New(nil),
		ID:              "rebind-" + t.Name(),
		SocketPath:      "/tmp/figaro-rebind-test.sock",
		Provider:        prov,
		ProviderFactory: f.build,
		Form:            cb,
	})
	t.Cleanup(a.Kill)
	ch, _ := subscribeChan(a)
	return a, ch
}

// TestProviderRebindsMidConversation is the regression test for the bug that
// stranded a live aria on a wedged provider: `system.provider` changed on the
// form (by `figaro set` or a re-applied outfit) but the agent kept the
// instance it was constructed with, so the switch only took effect if the
// whole agent was re-created. It must take effect on the next round.
func TestProviderRebindsMidConversation(t *testing.T) {
	f := newRebindFactory()
	a, ch := rebindAgent(t, f, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m-1"`),
		"system.provider": json.RawMessage(`"alpha"`),
	})

	runTurn(t, a, ch, "first")
	require.EqualValues(t, 1, f.inst("alpha", false).sends.Load())
	assert.Equal(t, "alpha", a.Info().Provider)

	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"beta"`),
		"system.model":    json.RawMessage(`"m-2"`),
	}}, 0)
	require.NoError(t, err)

	runTurn(t, a, ch, "second")

	assert.EqualValues(t, 1, f.inst("alpha", false).sends.Load(), "alpha must not serve the round after the switch")
	require.NotNil(t, f.inst("beta", false), "beta was never built")
	assert.EqualValues(t, 1, f.inst("beta", false).sends.Load(), "beta must serve the round after the switch")
	assert.Equal(t, "beta", a.Info().Provider, "status must report the live provider")
}

// A model change is NOT a rebuild: providers resolve system.model from the
// per-turn snapshot, and rebuilding would drop the in-memory wire projection
// for nothing.
func TestProviderModelChangeDoesNotRebuild(t *testing.T) {
	f := newRebindFactory()
	a, ch := rebindAgent(t, f, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m-1"`),
		"system.provider": json.RawMessage(`"alpha"`),
	})
	runTurn(t, a, ch, "first")

	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.model": json.RawMessage(`"m-2"`),
	}}, 0)
	require.NoError(t, err)
	runTurn(t, a, ch, "second")

	assert.Equal(t, []string{"alpha/false"}, f.builds(), "model change must not rebuild the provider")
	p := f.inst("alpha", false)
	assert.EqualValues(t, 2, p.sends.Load())
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Equal(t, []string{"m-1", "m-2"}, p.models, "the live model rides the snapshot")
}

// A build-time knob (here system.use_official_sdk) selects a different
// implementation behind the same provider name, so changing it must rebuild.
func TestProviderKnobChangeRebuilds(t *testing.T) {
	f := newRebindFactory()
	a, ch := rebindAgent(t, f, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m-1"`),
		"system.provider": json.RawMessage(`"alpha"`),
	})
	runTurn(t, a, ch, "first")

	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.use_official_sdk": json.RawMessage(`true`),
	}}, 0)
	require.NoError(t, err)
	runTurn(t, a, ch, "second")

	assert.Equal(t, []string{"alpha/false", "alpha/true"}, f.builds())
	assert.EqualValues(t, 1, f.inst("alpha", false).sends.Load())
	assert.EqualValues(t, 1, f.inst("alpha", true).sends.Load())
}

// A provider that cannot be built must fail the turn loudly, and must not
// silently keep talking to the old one, which is exactly the confusion the
// original bug produced. The aria stays alive and recovers when the board is
// corrected.
func TestProviderRebindFailureEndsTurnAndRecovers(t *testing.T) {
	f := newRebindFactory()
	a, ch := rebindAgent(t, f, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m-1"`),
		"system.provider": json.RawMessage(`"alpha"`),
	})
	runTurn(t, a, ch, "first")

	f.setFail("gamma", true)
	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"gamma"`),
	}}, 0)
	require.NoError(t, err)
	reason := runTurn(t, a, ch, "second")

	assert.EqualValues(t, 1, f.inst("alpha", false).sends.Load(), "the wedged provider must not serve a round meant for gamma")
	assert.Contains(t, reason, `provider "gamma"`, "the failure must name the provider that could not be built")

	// Correct the board: the aria is still usable.
	f.setFail("gamma", false)
	_, _, err = a.Set(form.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"beta"`),
	}}, 0)
	require.NoError(t, err)
	runTurn(t, a, ch, "third")
	require.NotNil(t, f.inst("beta", false))
	assert.EqualValues(t, 1, f.inst("beta", false).sends.Load())
}
