package figaro_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/message"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// newQueuedAgent parks a turn in the provider so prompts pile up in the inbox.
func newQueuedAgent(t *testing.T, id string) (*figaro.Agent, *blockedProvider, func()) {
	t.Helper()
	release := make(chan struct{})
	prov := &blockedProvider{release: release}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Form:      cb,
	})
	return a, prov, func() { close(release); a.Kill() }
}

func queuedTextsOf(a *figaro.Agent) []string {
	_, prompts := a.QueuedPrompts(false)
	out := make([]string, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Text)
	}
	return out
}

// Messages typed during a turn and then cut short are one question.
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

// Only an interrupt folds; queueing alone does not.
func TestSubmit_DoesNotCoalesce(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-2")
	defer done()

	a.SubmitPrompt(rpc.QuaRequest{Text: "kickoff"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond)

	a.SubmitPrompt(rpc.QuaRequest{Text: "one"})
	a.SubmitPrompt(rpc.QuaRequest{Text: "two"})

	for i := 0; i < 5; i++ {
		require.Equal(t, []string{"one", "two"}, queuedTextsOf(a))
		time.Sleep(10 * time.Millisecond)
	}
}

// Interrupt on an idle aria is a clean no-op: the aria is not left marked interrupted.
func TestInterrupt_IdleIsACleanNoOp(t *testing.T) {
	a, prov, done := newQueuedAgent(t, "coalesce-3")
	defer done()

	require.False(t, prov.blocked(), "no turn has been submitted yet")
	a.Interrupt()
	a.Interrupt()

	a.SubmitPrompt(rpc.QuaRequest{Text: "after the no-op"})
	require.Eventually(t, prov.blocked, 2*time.Second, 10*time.Millisecond,
		"an idle interrupt must not leave the aria refusing to run")
}

// A prompt arriving after the fold is its own event, never retroactively folded.
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

// A clearing hangup hands the queue back verbatim, one entry per message, so `figaro cut -j` is a save.
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

// The keep path reports the queue post-fold: what the aria will answer.
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

// Clearing is not gated on a live turn.
func TestHangup_ClearWorksWithNoTurnRunning(t *testing.T) {
	a, _, done := newQueuedAgent(t, "clear-2")
	defer done()

	resp := a.Hangup(rpc.QueueClear)
	require.True(t, resp.OK)
	assert.True(t, resp.Cleared)
	assert.Empty(t, resp.Queue)
}
