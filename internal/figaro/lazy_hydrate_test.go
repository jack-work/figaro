package figaro_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func runTurnsTo(t *testing.T, a *figaro.Agent, texts ...string) {
	t.Helper()
	ch, unsub := subscribeChan(a)
	defer unsub()
	for _, text := range texts {
		submitPrompt(a, text)
		timeout := time.After(5 * time.Second)
	wait:
		for {
			select {
			case n := <-ch:
				if n.Method == rpc.MethodTurnDone {
					break wait
				}
			case <-timeout:
				t.Fatal("timeout waiting for the turn")
			}
		}
	}
}

func wakeOn(t *testing.T, be store.Backend, id string) *figaro.Agent {
	t.Helper()
	snap, err := be.FormState(id)
	require.NoError(t, err)
	cb, _ := form.Open("")
	cb.Apply(snap.AsPatch())
	return figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        id,
		Provider:  &mockProvider{response: "reply"},
		Backend:   be,
		Form:      cb,
	})
}

func forkedWithHistory(t *testing.T) (store.Backend, string) {
	t.Helper()
	be, id := store.NewTestAria(t, "d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	parent := wakeOn(t, be, id)
	runTurnsTo(t, parent, "one", "two")
	parent.Kill()
	child, _, err := be.ForkWith(id, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"forked"`),
	}, nil))
	require.NoError(t, err)
	return be, child
}

func inquiries(p aria.Page) []string {
	var out []string
	for _, part := range p.Parts {
		out = append(out, part.Inquiry)
	}
	return out
}

func TestAWokenForkKeepsItsHistoryWhenItsFirstActIsAPrompt(t *testing.T) {
	be, child := forkedWithHistory(t)
	a := wakeOn(t, be, child)
	defer a.Kill()

	out, err := a.Handle(t.Context(), rpc.MethodQua, mustJSON(t, rpc.QuaRequest{Text: "three"}))
	require.NoError(t, err)
	require.Equal(t, 2, out.(rpc.QuaResponse).Cursor, "the cursor must name the forked-from turn before anything is read")

	ch, unsub := subscribeChan(a)
	defer unsub()
	timeout := time.After(5 * time.Second)
wait:
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				break wait
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	page := a.Read(aria.Anchor{}, 1<<20)
	require.Equal(t, []string{"one", "two", "three"}, inquiries(page))
}

func TestAWokenForkReadsItsHistoryOnDemand(t *testing.T) {
	be, child := forkedWithHistory(t)
	a := wakeOn(t, be, child)
	defer a.Kill()
	require.Equal(t, []string{"one", "two"}, inquiries(a.ReadBefore(aria.Anchor{}, aria.Anchor{}, 1<<20)))
	runTurnsTo(t, a, "three")
	require.Equal(t, []string{"one", "two", "three"}, inquiries(a.Read(aria.Anchor{}, 1<<20)))
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
