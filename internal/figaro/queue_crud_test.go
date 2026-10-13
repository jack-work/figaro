package figaro

import (
	"context"
	"github.com/jack-work/figaro/api/message"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

// A refusal must be specific enough to act on.

func TestQueueDelete_RemovesByID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})

	epoch, results := b.DeletePrompts(b.Epoch(), []uint64{1}, false)

	assert.Equal(t, b.Epoch(), epoch)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.QueueDeleted, results[0].Outcome)
	assert.Equal(t, []string{"two"}, promptTexts(b.SnapshotPrompts(true)))
}

// Deleting the message being committed is a legitimate refusal, and the caller is told which.
func TestQueueDelete_InFlightIsRefusedWithAReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	taken := b.TakeReadyUserPrompts() // the drain loop has it now

	_, results := b.DeletePrompts(b.Epoch(), []uint64{1}, false)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.QueueRejected, results[0].Outcome)
	assert.Equal(t, rpc.RejectCommitting, results[0].Reason)
	assert.NotEmpty(t, results[0].Detail)

	b.MarkCommitted(taken)
	_, results = b.DeletePrompts(b.Epoch(), []uint64{1}, false)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.RejectCommitted, results[0].Reason)
}

// A folded id resolves to the message that absorbed it.
func TestQueueDelete_MergedIDPointsAtItsSurvivor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})
	b.CoalesceUserPromptRuns()

	_, results := b.DeletePrompts(b.Epoch(), []uint64{2}, false)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.RejectMerged, results[0].Reason)
	assert.Equal(t, uint64(1), results[0].Into, "the caller is told where to aim instead")

	_, results = b.DeletePrompts(b.Epoch(), []uint64{1}, false)
	assert.Equal(t, rpc.QueueDeleted, results[0].Outcome)
	assert.Empty(t, b.SnapshotPrompts(true))
}

// An id from a previous generation must never resolve against this one.
func TestQueueDelete_StaleEpochMutatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	old := NewInbox(ctx)
	old.Send(event{typ: eventUserPrompt, text: "the message the client read"})
	staleEpoch := old.Epoch()

	fresh := NewInbox(ctx)
	fresh.Send(event{typ: eventUserPrompt, text: "somebody else's message"})

	_, results := fresh.DeletePrompts(staleEpoch, []uint64{1}, false)

	require.Len(t, results, 1)
	assert.Equal(t, rpc.QueueRejected, results[0].Outcome)
	assert.Equal(t, rpc.RejectStale, results[0].Reason)
	assert.Equal(t, []string{"somebody else's message"}, promptTexts(fresh.SnapshotPrompts(true)),
		"a stale id must not delete the message that now holds that number")
}

// An epoch is required whenever ids are named.
func TestQueueDelete_MissingEpochIsStale(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})

	_, results := b.DeletePrompts("", []uint64{1}, false)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.RejectStale, results[0].Reason)
	require.Len(t, b.SnapshotPrompts(true), 1)
}

// The all-form needs no epoch and reports one result per message removed; control events stay.
func TestQueueDelete_AllNeedsNoEpochAndSparesControlEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: "two"})

	_, results := b.DeletePrompts("", nil, true)

	require.Len(t, results, 2)
	assert.Equal(t, rpc.QueueDeleted, results[0].Outcome)
	assert.Empty(t, b.SnapshotPrompts(true))
	require.Len(t, b.pending(), 1, "a queued set is not a question and is not dropped")
	assert.Equal(t, eventStudyMark, b.pending()[0].typ)
}

func TestQueueDelete_UnknownID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})

	_, results := b.DeletePrompts(b.Epoch(), []uint64{99}, false)
	require.Len(t, results, 1)
	assert.Equal(t, rpc.RejectUnknown, results[0].Reason)
}

// Results come back one per requested id, in request order.
func TestQueueDelete_OneResultPerRequestedID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventUserPrompt, text: "two"})

	_, results := b.DeletePrompts(b.Epoch(), []uint64{2, 99, 1}, false)
	require.Len(t, results, 3)
	assert.Equal(t, uint64(2), results[0].ID)
	assert.Equal(t, rpc.QueueDeleted, results[0].Outcome)
	assert.Equal(t, uint64(99), results[1].ID)
	assert.Equal(t, rpc.RejectUnknown, results[1].Reason)
	assert.Equal(t, uint64(1), results[2].ID)
	assert.Equal(t, rpc.QueueDeleted, results[2].Outcome)
}

func TestQueueUpdate_RewritesQueuedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "typo"})

	_, result := b.UpdatePrompt(b.Epoch(), 1, "fixed")
	assert.Equal(t, rpc.QueueUpdated, result.Outcome)
	assert.Equal(t, []string{"fixed"}, promptTexts(b.SnapshotPrompts(true)))

	_, result = b.UpdatePrompt("wrong-epoch", 1, "nope")
	assert.Equal(t, rpc.RejectStale, result.Reason)
	_, result = b.UpdatePrompt(b.Epoch(), 42, "nope")
	assert.Equal(t, rpc.RejectUnknown, result.Reason)
	assert.Equal(t, []string{"fixed"}, promptTexts(b.SnapshotPrompts(true)))
}
