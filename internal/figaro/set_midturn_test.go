package figaro_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// snapshotWatchProvider records the board each send was given and blocks in
// the middle of one, so a set can land while a turn is in flight.
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

	// Hold the FIRST send open, so the set below lands mid-turn.
	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
	mockPushAssistant(in.FigLog, nil, bus, func(_ message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[]}`)}, nil
	}, p.Fingerprint(), "ok")
	return nil
}

// A SET THAT LANDS MID-TURN DOES NOT CHANGE THE REQUEST IN FLIGHT, AND THE
// NEXT TURN SEES IT.
//
// This is the ONE property the queue hop bought: bound-form sets rode the
// figaro's own inbox and were applied at a round boundary, so a set could not
// move the board under a running turn. Sending them straight to the form's
// actor removes that serialization, so the property has to be true for a
// different reason -- the turn samples the board ONCE, at send.
//
// If this goes red, the board is being re-read mid-turn and unqueueing a set
// is a correctness change rather than a scheduling one.
func TestASetMidTurnDoesNotMoveTheBoardUnderTheTurnInFlight(t *testing.T) {
	p := &snapshotWatchProvider{entered: make(chan struct{}), release: make(chan struct{})}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
	be, id := store.NewTestAria(t, "d", message.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: id, SocketPath: "/tmp/test-set-midturn.sock",
		Provider: p, Backend: be, Form: cb,
	})
	defer a.Kill()

	_, _, err := a.Set(form.Patch{Set: map[string]json.RawMessage{
		"probe": json.RawMessage(`"before"`),
	}}, 0)
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

	// THE TURN IS RUNNING. Move the board.
	_, _, err = a.Set(form.Patch{Set: map[string]json.RawMessage{
		"probe": json.RawMessage(`"during"`),
	}}, 0)
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
		t.Errorf("the in-flight request saw probe=%s, want before: the board moved under it", p.seen[0])
	}
	if p.seen[1] != "during" {
		t.Errorf("the next turn saw probe=%s, want during: the set never reached a turn", p.seen[1])
	}
}
