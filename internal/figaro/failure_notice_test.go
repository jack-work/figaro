package figaro_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
)

// gluck, 0.57.1: the figaros that sent the three failed turns heard nothing.
// A failed turn tells each figaro whose message it was answering, once, in
// fixed words with a closed category: the reason is upstream text and stays
// on the failing figaro's form. A notice whose own turn fails tells no one,
// so two failing figaros cannot ping-pong.

type failureEar struct {
	mu   sync.Mutex
	told []figaro.FailureNotice
}

func (e *failureEar) tell(n figaro.FailureNotice) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.told = append(e.told, n)
}

func (e *failureEar) heard() []figaro.FailureNotice {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]figaro.FailureNotice(nil), e.told...)
}

const (
	senderA = "aaaaaaaa11111111aaaaaaaa11111111"
	senderB = "bbbbbbbb22222222bbbbbbbb22222222"
)

func listeningAgent(t *testing.T) (*figaro.Agent, *flakyProvider, *failureEar) {
	t.Helper()
	ear := &failureEar{}
	a, p, _ := flakyAgent(t, func(c *figaro.Config) { c.TellFailure = ear.tell })
	return a, p, ear
}

func TestAFailedTurnTellsItsFigaroSenderOnce(t *testing.T) {
	a, _, ear := listeningAgent(t)
	ch, _ := subscribeChan(a)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "please"}, senderA))
	waitTurnDone(t, ch)

	got := ear.heard()
	require.Len(t, got, 1, "the figaro that sent the failed turn's message was told %d times", len(got))
	require.Equal(t, senderA, got[0].To)
	require.Equal(t, a.ID(), got[0].From)
	require.Equal(t, "figaro "+a.ID()+": turn 1 failed (rate_limited). The reason is in system.failure.reason on its form.", got[0].Text)
	require.NotContains(t, got[0].Text, "7-day", "the notice carries the upstream's words into another figaro's prompt")
}

func TestAFailedTurnTellsNoOneWhoIsNotAFigaro(t *testing.T) {
	a, _, ear := listeningAgent(t)
	ch, _ := subscribeChan(a)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "please"}, "Gluck"))
	waitTurnDone(t, ch)
	require.Empty(t, ear.heard())
}

func TestAFailedNoticeTellsNoOne(t *testing.T) {
	a, _, ear := listeningAgent(t)
	ch, _ := subscribeChan(a)
	require.NoError(t, a.SubmitFailureNotice(senderB, "turn 3 failed: anthropic: 7-day rate limit reached"))
	waitTurnDone(t, ch)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, ear.heard(), "a failure notice that failed was answered with another notice")
}

func TestAGoodTurnTellsNoOne(t *testing.T) {
	a, p, ear := listeningAgent(t)
	p.fail.Store(false)
	ch, _ := subscribeChan(a)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "please"}, senderA))
	waitTurnDone(t, ch)
	require.Empty(t, ear.heard())
}

type gatedFailure struct {
	mockProvider
	entered chan struct{}
	release chan struct{}
}

func (p *gatedFailure) Send(ctx context.Context, _ provider.SendInput, _ provider.Bus) error {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return &provider.RateLimited{Provider: "anthropic", Window: "7-day"}
}

// Two messages from one figaro folded into one turn are one failed turn.
func TestFoldedMessagesFromOneFigaroAreToldOnce(t *testing.T) {
	ear := &failureEar{}
	gate := &gatedFailure{entered: make(chan struct{}, 1), release: make(chan struct{})}
	a, _, _ := flakyAgent(t, func(c *figaro.Config) { c.TellFailure = ear.tell; c.Provider = gate })
	ch, _ := subscribeChan(a)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "one"}, senderA))
	<-gate.entered
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "two"}, senderA))
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "three"}, senderA))
	close(gate.release)
	waitTurnDone(t, ch)
	waitTurnDone(t, ch)
	require.Len(t, ear.heard(), 2, "two failed turns, one of them folding two messages from the same figaro: %+v", ear.heard())
}
