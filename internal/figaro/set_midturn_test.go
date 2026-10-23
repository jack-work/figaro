package figaro_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// snapshotWatchProvider records the bound form each send was given and blocks mid-send.
type snapshotWatchProvider struct {
	mu      sync.Mutex
	seen    []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *snapshotWatchProvider) Name() string        { return "mock" }
func (p *snapshotWatchProvider) Fingerprint() string { return "mock/v0" }
func (p *snapshotWatchProvider) SetModel(string)     {}
func (p *snapshotWatchProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *snapshotWatchProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	got := ""
	if v := in.Snapshot.Lookup("probe"); v != nil {
		got = *v
	}
	p.mu.Lock()
	p.seen = append(p.seen, got)
	p.mu.Unlock()

	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
	mockPushAssistant(in.FigLog, nil, bus, func(_ message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[]}`)}, nil
	}, p.Fingerprint(), "ok")
	return nil
}

// A set landing mid-turn does not change the request in flight, and the
// next turn sees it: the turn samples the bound form once, at send.
func TestASetMidTurnDoesNotMoveTheBoundFormUnderTheTurnInFlight(t *testing.T) {
	p := &snapshotWatchProvider{entered: make(chan struct{}), release: make(chan struct{})}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	be, id := store.NewTestAria(t, "d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: id,
		Provider: p, Backend: be, Form: cb,
	})
	defer a.Kill()

	_, _, err := a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"probe": json.RawMessage(`"before"`),
	}, nil), 0)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		v := a.Snapshot().Lookup("probe")
		return v != nil && *v == "before"
	}, 2*time.Second, 10*time.Millisecond, "the first set never landed")

	submitPrompt(a, "one")
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the provider never entered Send")
	}

	_, _, err = a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"probe": json.RawMessage(`"during"`),
	}, nil), 0)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond) // let it land if it is going to
	close(p.release)

	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.seen) == 1
	}, 3*time.Second, 10*time.Millisecond, "the first turn never finished")

	submitPrompt(a, "two")
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.seen) == 2
	}, 5*time.Second, 10*time.Millisecond, "the second turn never ran")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen[0] != "before" {
		t.Errorf("the in-flight request saw probe=%s, want before: the bound form moved under it", p.seen[0])
	}
	if p.seen[1] != "during" {
		t.Errorf("the next turn saw probe=%s, want during: the set never reached a turn", p.seen[1])
	}
}
