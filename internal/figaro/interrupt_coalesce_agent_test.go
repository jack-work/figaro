package figaro_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/uiir"
)

// newQueuedAgent parks a turn in the provider so prompts pile up in the inbox
// verifiably, which is the only state in which the interrupt-time fold means
// anything.
func newQueuedAgent(t *testing.T, id string) (*figaro.Agent, *blockedProvider, func()) {
	t.Helper()
	release := make(chan struct{})
	prov := &blockedProvider{release: release}
	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}})
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		SocketPath: "/tmp/test-" + id + ".sock",
		Provider:   prov,
		Chalkboard: cb,
	})
	return a, prov, func() { close(release); a.Kill() }
}

// queuedTextsOf reads the display view of the queue.
func queuedTextsOf(a *figaro.Agent) []string {
	_, prompts := a.QueuedPrompts(false)
	out := make([]string, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Text)
	}
	return out
}

// ITEM 1. Messages typed during a long turn and then cut short are ONE
// question, not a queue of turns to sit through.
func TestInterrupt_CoalescesTheWaitingQueue(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-1")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)

	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "three"})
	require.Equal(t, []string{"one", "two", "three"}, queuedTextsOf(a))

	a.Interrupt()

	epoch, prompts := a.QueuedPrompts(false)
	require.NotEmpty(t, epoch)
	require.Len(t, prompts, 1, "the whole waiting run is one message")
	assert.Equal(t, "one\n\ntwo\n\nthree", prompts[0].Text)
	assert.Equal(t, uint64(2), prompts[0].ID, "the survivor keeps the first id of the run")
	assert.Equal(t, []uint64{3, 4}, prompts[0].Merged, "and names the ids it absorbed")
}

// A NORMAL SUBMIT IS UNTOUCHED. Nothing about queueing three prompts folds
// them; only an interrupt does. This is the guarantee that the fold is on the
// interrupt path and nowhere else.
func TestSubmit_DoesNotCoalesce(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-2")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)

	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})

	// Sample repeatedly: a fold triggered by anything other than an interrupt
	// would show up as the queue collapsing on its own.
	for i := 0; i < 5; i++ {
		require.Equal(t, []string{"one", "two"}, queuedTextsOf(a))
		time.Sleep(10 * time.Millisecond)
	}
}

// An IDLE aria has no turn to interrupt, so Interrupt returns before it
// reaches the inbox at all — the fold requires a live turn by construction
// (Agent.Interrupt's early return), and an idle aria's queue is being drained
// by the actor anyway. What matters at this level is that the no-op is clean:
// the aria is not left marked interrupted, so the next turn is not poisoned.
func TestInterrupt_IdleIsACleanNoOp(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-3")
	defer done()

	require.False(t, prov.blocked(), "no turn has been submitted yet")
	a.Interrupt()
	a.Interrupt()

	// The aria still works: a prompt submitted after an idle interrupt opens a
	// turn normally rather than being aborted on arrival.
	a.SubmitPrompt(rpc.QuaRequest{Text: "after the no-op"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond,
		"an idle interrupt must not leave the aria refusing to run")
}

// THE RACE, STATED AND PINNED. A prompt that arrives after the fold is its own
// event and is never retroactively folded; it is classified at the drain
// boundary like any other prompt.
func TestInterrupt_PromptArrivingAfterTheFoldStaysSeparate(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-4")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)

	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})
	a.Interrupt()

	a.SubmitPrompt(rpc.QuaRequest{Text: "late"})

	texts := queuedTextsOf(a)
	require.Len(t, texts, 2, "the late prompt is a separate event")
	assert.Equal(t, "one\n\ntwo", texts[0])
	assert.Equal(t, "late", texts[1])
}

// ITEM 2. A clearing hangup drops the queue and hands it back VERBATIM — one
// entry per message as typed, each with its own id — so `figaro cut -j` is a
// save, not a lament. Coalescing first would return one blob and defeat that.
func TestHangup_ClearDrainsVerbatim(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "clear-1")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)
	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})

	resp := a.Hangup(rpc.QueueClear)

	require.True(t, resp.OK)
	assert.True(t, resp.Cleared)
	assert.NotEmpty(t, resp.Epoch)
	require.Len(t, resp.Queue, 2, "the drained messages come back one for one")
	assert.Equal(t, "one", resp.Queue[0].Text)
	assert.Equal(t, "two", resp.Queue[1].Text)
	assert.NotEqual(t, resp.Queue[0].ID, resp.Queue[1].ID, "each keeps its own id")
	assert.Empty(t, queuedTextsOf(a), "and the aria is left with nothing to answer")
}

// The keep path reports the queue as of the hangup — post-fold, because that
// is what the aria will actually answer.
func TestHangup_KeepReportsTheFoldedQueue(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "keep-1")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)
	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})

	resp := a.Hangup(rpc.QueueKeep)

	require.True(t, resp.OK)
	assert.False(t, resp.Cleared, "keep must never report itself as cleared")
	require.Len(t, resp.Queue, 1)
	assert.Equal(t, "one\n\ntwo", resp.Queue[0].Text)
}

// Clearing is not gated on a live turn: a queue can be worth dropping between
// turns, and refusing then would mean nothing to the person asking.
func TestHangup_ClearWorksWithNoTurnRunning(t *testing.T) {
	a, _, done := newQueuedAgent(t, "clear-2")
	defer done()

	resp := a.Hangup(rpc.QueueClear)
	require.True(t, resp.OK)
	assert.True(t, resp.Cleared)
	assert.Empty(t, resp.Queue)
}
