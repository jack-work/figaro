package figaro

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

// Unit half of the interrupt-time fold; the agent-level half is in interrupt_coalesce_agent_test.go.

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
	// The survivor keeps the first id and names what it absorbed.
	assert.Equal(t, uint64(1), snap[0].id)
	assert.Equal(t, []uint64{2, 3}, snap[0].merged)
}

// A queued form set is a barrier: a prompt behind it must not be answered against earlier state.
func TestCoalesce_AQueuedSetBlocksTheFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: "three"})

	b.CoalesceUserPromptRuns()

	require.Len(t, b.pending(), 3, "the set must survive between the two runs")
	assert.Equal(t, eventUserPrompt, b.pending()[0].typ)
	assert.Equal(t, "one\n\ntwo", b.pending()[0].text)
	assert.Equal(t, eventStudyMark, b.pending()[1].typ, "order across event kinds is preserved")
	assert.Equal(t, eventUserPrompt, b.pending()[2].typ)
	assert.Equal(t, "three", b.pending()[2].text, "a prompt behind a set is NOT folded in front of it")
}

// A carrier folds into its run; its patch rides the combined message in queue order.
func TestCoalesce_CarrierPatchesRideTheCombinedMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, form: &rpc.FormInput{
		Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}, nil)),
	}})

	b.CoalesceUserPromptRuns()

	snap := b.SnapshotPrompts(true)
	require.Len(t, snap, 1)
	assert.Equal(t, "one", snap[0].text, "an empty-text carrier contributes no line")
	require.NotNil(t, snap[0].form)
	require.NotNil(t, snap[0].form.Patch)
	_, hasK := snap[0].form.Patch.Entry("k")
	assert.True(t, hasK)
}

// A single prompt is left exactly as it was, id included.
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

func ptrPatch(p form.Patch) *form.Patch { return &p }
