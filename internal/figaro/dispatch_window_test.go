package figaro_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

type heldTurnStart struct {
	*store.XwalBackend
	entered chan struct{}
	release chan struct{}
}

func (b *heldTurnStart) CloseOpenToolCalls(id string) (int, error) {
	select {
	case b.entered <- struct{}{}:
		<-b.release
	default:
	}
	return b.XwalBackend.CloseOpenToolCalls(id)
}

// A prompt taken off the inbox but whose turn has not yet opened is still
// work in hand. Reporting idle in that window let a hibernate (or a move)
// reclaim the aria under an accepted prompt; TestHibernateWakeCycle caught
// the other ordering as "is active" right after polling idle.
func TestAPromptBetweenTheInboxAndItsTurnKeepsTheAriaActive(t *testing.T) {
	snap := map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, snap, nil))
	be, id := store.NewTestAria(t, "d", form.Build(form.Snapshot{}, snap, nil))
	held := &heldTurnStart{XwalBackend: be, entered: make(chan struct{}), release: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        id,
		Provider:  &mockProvider{response: "ok"},
		Backend:   held,
		Form:      cb,
	})
	t.Cleanup(a.Kill)
	var release sync.Once
	letGo := func() { release.Do(func() { close(held.release) }) }
	t.Cleanup(letGo)

	a.SubmitPrompt(rpc.QuaRequest{Text: "hello"})
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never started")
	}
	require.Equal(t, "active", a.Info().State, "a prompt in dispatch reported idle")
	letGo()
	require.Eventually(t, func() bool { return a.Info().State == "idle" }, 5*time.Second, 10*time.Millisecond)
}
