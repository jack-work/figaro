package figaro_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type gatedProvider struct {
	*spoolProvider
	gate chan struct{}
	mu   sync.Mutex
	n    int
}

func (p *gatedProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.n++
	n := p.n
	p.mu.Unlock()
	if n == 2 {
		<-p.gate
	}
	return p.spoolProvider.Send(ctx, in, bus)
}

func (p *gatedProvider) sends() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func askedOn(t *testing.T, be store.Backend, id string) []message.Message {
	t.Helper()
	log, err := be.OpenFigIR(id)
	require.NoError(t, err)
	var out []message.Message
	for _, e := range log.Read() {
		if m := e.Payload; m.Role == message.RoleInput && len(m.Content) > 0 && !m.Steering {
			out = append(out, m)
		}
	}
	return out
}

func replayBehindALiveTurn(t *testing.T) (store.Backend, string, *figaro.Agent, *gatedProvider) {
	t.Helper()
	be, id := backedConv(t, t.TempDir())
	p := &gatedProvider{spoolProvider: newSpoolProvider(false), gate: make(chan struct{})}
	a := boundAgent(t, be, id, nil, p)
	t.Cleanup(a.Kill)
	a.SubmitPrompt(rpc.QuaRequest{Text: "the recorded question"})
	p.await(t)
	awaitState(t, a, "idle")
	a.SubmitPrompt(rpc.QuaRequest{Text: "live work"})
	require.Eventually(t, func() bool { return p.sends() == 2 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, a.ApplySuccession(form.Replay{Of: id, Turn: 1}.Patch(form.Snapshot{})))
	require.NoError(t, a.SubmitReplay())
	return be, id, a, p
}

// Defends #114: a replay runs alone. A prompt queued behind it is its own
// turn, so the replayed inquiry carries none of its words.
func TestAReplayRunsAloneBeforeWhatQueuedBehindIt(t *testing.T) {
	be, id, a, p := replayBehindALiveTurn(t)
	require.NoError(t, a.SubmitSuccessionPrompt("figaro", "late words", false))
	close(p.gate)
	require.Eventually(t, func() bool { return len(askedOn(t, be, id)) == 4 }, 5*time.Second, 5*time.Millisecond)
	awaitState(t, a, "idle")
	got := askedOn(t, be, id)
	require.NotNil(t, got[2].Replay, "the replay was folded away")
	require.Equal(t, "the recorded question", got[2].Content[0].Text)
	require.Len(t, got[2].Content, 1, "the replayed inquiry carries a queued prompt's words")
	require.Nil(t, got[3].Replay, "a live prompt ran as a replay")
	require.Equal(t, "late words", got[3].Content[0].Text)
}

// Defends #114: unset before it runs, a replay is dropped and leaves the queue.
func TestAReplayUnsetBeforeItRunsIsDropped(t *testing.T) {
	be, id, a, p := replayBehindALiveTurn(t)
	require.NoError(t, a.ApplySuccession(form.Build(form.Snapshot{}, nil, []string{form.ReplayKey})))
	close(p.gate)
	awaitState(t, a, "idle")
	time.Sleep(100 * time.Millisecond)
	for _, m := range askedOn(t, be, id) {
		require.Nil(t, m.Replay, "an unset replay ran")
	}
	_, queued := a.QueuedPrompts(true)
	require.Empty(t, queued, "the dropped replay is still in the queue")
	require.Equal(t, 2, p.sends())
}
