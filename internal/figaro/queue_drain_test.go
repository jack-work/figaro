package figaro

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fold is a property of DRAINING, not of interrupting. These pin the two
// rules that were wrong: what the idle drain takes, and what a cancelled turn
// is allowed to take.

// Messages queued behind a turn that has FINISHED are one question, not one
// turn each. This is the reported case: four sends chained with && against an
// idle aria — the first opens a turn, the rest pile up behind it.
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

// A LONE submit is still exactly itself — one message, its own id, no folding.
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
// run, so a prompt is never answered against a chalkboard it never saw.
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
	require.Len(t, b.queue, 2, "the set and the prompt behind it stay queued")
	assert.Equal(t, eventSet, b.queue[0].typ)
}
