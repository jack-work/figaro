package figaro

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fold is a property of DRAINING, not of interrupting. These pin the two
// rules that were wrong: what the idle drain takes, and what a cancelled turn
// is allowed to take.

// Messages queued behind a turn that has FINISHED are one question, not one
// turn each. This is the reported case: four sends chained with && against an
// idle aria: the first opens a turn, the rest pile up behind it.
func TestDrain_QueuedRunBecomesOneInquiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "test2"})
	b.Send(event{typ: eventUserPrompt, text: "test3"})
	b.Send(event{typ: eventUserPrompt, text: "test4"})

	// What act() does: take one, then the contiguous run behind it.
	first, ok := b.Recv()
	require.True(t, ok)
	batch := append([]event{first}, b.TakeReadyUserPrompts()...)
	merged, ok := mergePromptEvents(batch)
	require.True(t, ok)

	assert.Equal(t, "test2\n\ntest3\n\ntest4", merged.text)
	assert.True(t, b.IsIdle(), "the whole run is drained, not one message of it")
}

// A LONE submit is still exactly itself: one message, its own id, no folding.
func TestDrain_LoneSubmitIsUntouched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "test1"})
	first, ok := b.Recv()
	require.True(t, ok)
	batch := append([]event{first}, b.TakeReadyUserPrompts()...)
	merged, ok := mergePromptEvents(batch)
	require.True(t, ok)

	assert.Equal(t, "test1", merged.text)
	assert.Equal(t, first.id, merged.id)
	assert.Empty(t, merged.merged)
}

// The barrier survives at this drain site too: a queued set still splits the
// run, so a prompt is never answered against a form it never saw.
func TestDrain_SetStillBlocksTheIdleFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.Send(event{typ: eventSet})
	b.Send(event{typ: eventUserPrompt, text: "three"})

	first, _ := b.Recv()
	batch := append([]event{first}, b.TakeReadyUserPrompts()...)
	merged, _ := mergePromptEvents(batch)

	assert.Equal(t, "one\n\ntwo", merged.text)
	require.Len(t, b.pending(), 2, "the set and the prompt behind it stay queued")
	assert.Equal(t, eventSet, b.pending()[0].typ)
}

// THE BUG BEHIND "the messages were received, but the turn ended abruptly".
//
// A drain that runs after the cancel appends the queued messages to the log -
// so they appear on screen, "received", and hands them to a round that opens
// with an already-dead context. That round dies at once and the messages go
// with it: committed to the conversation, never answered, and gone from the
// queue that would have re-asked them.
//
// The rule is that a cancelled turn takes nothing with it.
func TestDrain_InterruptedTurnDoesNotSwallowTheQueue(t *testing.T) {
	for _, drain := range []struct {
		name string
		call func(a *Agent) error
	}{
		{"appendSteeringPrompts", (*Agent).appendSteeringPrompts},
		{"prepareProviderRound", (*Agent).prepareProviderRound},
	} {
		t.Run(drain.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := newDrainTestAgent(t, ctx)

			base := a.figLog.Len() // genesis and the birth record
			a.inbox.Send(event{typ: eventUserPrompt, text: "one"})
			a.inbox.Send(event{typ: eventUserPrompt, text: "two"})

			// Not interrupted: the drain does its job, as it always has.
			require.NoError(t, drain.call(a))
			require.True(t, a.inbox.IsIdle(), "an uninterrupted drain takes the batch")
			require.Equal(t, base+1, a.figLog.Len(), "and appends it as ONE message")

			// Interrupted: it takes nothing.
			a.inbox.Send(event{typ: eventUserPrompt, text: "three"})
			a.inbox.Send(event{typ: eventUserPrompt, text: "four"})
			a.mu.Lock()
			a.interrupted = true
			a.mu.Unlock()

			require.NoError(t, drain.call(a))
			assert.False(t, a.inbox.IsIdle(),
				"a cancelled turn must leave the queue for the next turn to ask")
			assert.Equal(t, base+1, a.figLog.Len(),
				"and must not commit messages it cannot answer")
			assert.Equal(t, []string{"three", "four"},
				promptTexts(a.inbox.SnapshotPrompts(true)))
		})
	}
}

// newDrainTestAgent builds the smallest Agent the drain paths touch, WITHOUT
// starting the actor: the point is to call the drain deliberately, and a
// running drain loop would race the test for its own queue.
func newDrainTestAgent(t *testing.T, ctx context.Context) *Agent {
	t.Helper()
	board, _ := form.Open("")
	be, id := store.NewTestAria(t, "d", message.Patch{})
	log, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{
		id:          id,
		backend:     be,
		inbox:       NewInbox(ctx),
		figLog:      log,
		form:        board,
		ariaSrv:     aria.NewServer(),
		gov:         toolout.New(liveOutputTail),
		argPartials: map[string]string{},
	}
}
