package figaro

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/rpc"
)

// The interrupt-time fold. These are the unit-level half; the agent-level
// behaviour (an interrupt actually folding a live queue, and an idle aria
// folding nothing) lives in interrupt_coalesce_agent_test.go.

func TestCoalesce_FoldsAContiguousRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.Send(event{typ: eventUserPrompt, text: "three"})

	b.CoalesceUserPromptRuns()

	snap := b.SnapshotPrompts(true)
	require.Len(t, snap, 1, "a contiguous run is one message")
	assert.Equal(t, "one\n\ntwo\n\nthree", snap[0].text)
	// Identity folds with the content: the survivor keeps the first id and
	// names what it absorbed, so a client holding id 2 can still find it.
	assert.Equal(t, uint64(1), snap[0].id)
	assert.Equal(t, []uint64{2, 3}, snap[0].merged)
}

// THE RULING, EXECUTABLE. A queued chalkboard set is a barrier: it exists to
// change context before the prompt behind it, and folding that prompt in front
// of it would answer the question against state it was never written against.
func TestCoalesce_AQueuedSetBlocksTheFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.Send(event{typ: eventSet})
	b.Send(event{typ: eventUserPrompt, text: "three"})

	b.CoalesceUserPromptRuns()

	require.Len(t, b.queue, 3, "the set must survive between the two runs")
	assert.Equal(t, eventUserPrompt, b.queue[0].typ)
	assert.Equal(t, "one\n\ntwo", b.queue[0].text)
	assert.Equal(t, eventSet, b.queue[1].typ, "order across event kinds is preserved")
	assert.Equal(t, eventUserPrompt, b.queue[2].typ)
	assert.Equal(t, "three", b.queue[2].text, "a prompt behind a set is NOT folded in front of it")
}

// Same rule, higher stakes: coalescing across a fork would move a message into
// the wrong trunk.
func TestCoalesce_AQueuedForkBlocksTheFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "before"})
	b.Send(event{typ: eventFork})
	b.Send(event{typ: eventUserPrompt, text: "after-a"})
	b.Send(event{typ: eventUserPrompt, text: "after-b"})

	b.CoalesceUserPromptRuns()

	require.Len(t, b.queue, 3)
	assert.Equal(t, "before", b.queue[0].text)
	assert.Equal(t, eventFork, b.queue[1].typ)
	assert.Equal(t, "after-a\n\nafter-b", b.queue[2].text, "the run AFTER the fork folds on its own")
}

// A carrier (empty text, chalkboard only) folds into the run it sits in: its
// patch rides on the combined message, in queue order, so a later value wins.
func TestCoalesce_CarrierPatchesRideTheCombinedMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, chalkboard: &rpc.ChalkboardInput{
		Patch: &rpc.ChalkboardPatch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}},
	}})

	b.CoalesceUserPromptRuns()

	snap := b.SnapshotPrompts(true)
	require.Len(t, snap, 1)
	assert.Equal(t, "one", snap[0].text, "an empty-text carrier contributes no line")
	require.NotNil(t, snap[0].chalkboard)
	require.NotNil(t, snap[0].chalkboard.Patch)
	assert.Contains(t, snap[0].chalkboard.Patch.Set, "k")
}

// Nothing to fold is not an error, and a single prompt is left exactly as it
// was — including its id, which a client may already be holding.
func TestCoalesce_IsANoOpOnAShortQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.CoalesceUserPromptRuns()
	assert.True(t, b.IsIdle())

	b.Send(event{typ: eventUserPrompt, text: "only"})
	b.CoalesceUserPromptRuns()
	snap := b.SnapshotPrompts(false)
	require.Len(t, snap, 1)
	assert.Equal(t, uint64(1), snap[0].id)
	assert.Empty(t, snap[0].merged)
}
