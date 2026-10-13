package figaro

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fold is a property of draining, not of interrupting.

// Messages queued behind a finished turn are one question.
func TestDrain_QueuedRunBecomesOneInquiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "test2"})
	b.Send(event{typ: eventUserPrompt, text: "test3"})
	b.Send(event{typ: eventUserPrompt, text: "test4"})

	first, ok := b.Recv()
	require.True(t, ok)
	batch := append([]event{first}, b.TakeReadyUserPrompts()...)
	merged, ok := mergePromptEvents(batch)
	require.True(t, ok)

	assert.Equal(t, "test2\n\ntest3\n\ntest4", merged.text)
	assert.True(t, b.IsIdle(), "the whole run is drained, not one message of it")
}

// A lone submit is exactly itself.
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

// A queued set still splits the run, so a prompt is never answered against a form it never saw.
func TestDrain_SetStillBlocksTheIdleFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: "three"})

	first, _ := b.Recv()
	batch := append([]event{first}, b.TakeReadyUserPrompts()...)
	merged, _ := mergePromptEvents(batch)

	assert.Equal(t, "one\n\ntwo", merged.text)
	require.Len(t, b.pending(), 2, "the set and the prompt behind it stay queued")
	assert.Equal(t, eventStudyMark, b.pending()[0].typ)
}

// A drain after the cancel hands queued messages to a round with a dead
// context: committed, never answered, gone from the queue. A cancelled turn takes nothing.
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

			require.NoError(t, drain.call(a))
			require.True(t, a.inbox.IsIdle(), "an uninterrupted drain takes the batch")
			require.Equal(t, base+1, a.figLog.Len(), "and appends it as ONE message")

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

// newDrainTestAgent does not start the actor: a running drain loop would race the test for its queue.
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
