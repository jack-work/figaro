package figaro

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Stage 2 collapsed Patient/Selfish/Yield into a plain FIFO. Tests
// here cover the surviving surface: enqueue, dequeue, close.

func TestInbox_FIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "first"})
	b.Send(event{typ: eventUserPrompt, text: "second"})

	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, "first", evt.text)

	evt, ok = b.Recv()
	require.True(t, ok)
	assert.Equal(t, "second", evt.text)
}

func TestInbox_TakeReadySetContiguousPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "p"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})

	// A set behind a prompt is not taken until the prompt clears: the
	// drain loop preserves FIFO across kinds.
	assert.Empty(t, b.TakeReadySet())
	require.Len(t, b.TakeReadyUserPrompts(), 1)
	require.Len(t, b.TakeReadySet(), 1)
	assert.True(t, b.IsIdle())
}

func TestInbox_SetPromptSetBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: "mid"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})

	require.Len(t, b.TakeReadySet(), 1) // leading set
	assert.Empty(t, b.TakeReadySet())   // now blocked by the prompt
	p := b.TakeReadyUserPrompts()
	require.Len(t, p, 1)
	assert.Equal(t, "mid", p[0].text)
	require.Len(t, b.TakeReadySet(), 1) // trailing set
	assert.True(t, b.IsIdle())
}

func TestRenderablePromptDetection(t *testing.T) {
	assert.False(t, hasRenderablePrompt([]event{{typ: eventUserPrompt}}))
	assert.True(t, hasRenderablePrompt([]event{{typ: eventUserPrompt, text: "visible"}}))
}

func TestInbox_RecvBlocksWhenEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	done := make(chan bool, 1)
	go func() {
		_, ok := b.Recv()
		done <- ok
	}()

	select {
	case <-done:
		t.Fatal("Recv should block on empty inbox")
	case <-time.After(50 * time.Millisecond):
	}

	b.Send(event{typ: eventUserPrompt, text: "wakeup"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Recv should have unblocked after Send")
	}
}

func TestInbox_IsIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	assert.True(t, b.IsIdle())
	b.Send(event{typ: eventUserPrompt})
	assert.False(t, b.IsIdle())
	_, _ = b.Recv()
	assert.True(t, b.IsIdle())
}

func TestInbox_CloseUnblocksRecv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	done := make(chan bool, 1)
	go func() {
		_, ok := b.Recv()
		done <- ok
	}()

	b.Close()

	select {
	case ok := <-done:
		assert.False(t, ok, "Recv should return false after Close")
	case <-time.After(time.Second):
		t.Fatal("Recv should have unblocked after Close")
	}
}

func TestInbox_SendAfterCloseReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	b.Close()

	assert.False(t, b.Send(event{typ: eventUserPrompt}))
}

func TestInbox_ContextCancelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := NewInbox(ctx)

	done := make(chan bool, 1)
	go func() {
		_, ok := b.Recv()
		done <- ok
	}()

	cancel()

	select {
	case ok := <-done:
		assert.False(t, ok, "Recv should return false after context cancel")
	case <-time.After(time.Second):
		t.Fatal("Recv should have unblocked after context cancel")
	}
}

func TestInbox_SnapshotPromptsFIFOAndReadOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	// A pure-form prompt (empty text) is a carrier, not a message -
	// the default snapshot omits it. Non-prompt events (Set, Fork) are also
	// skipped.
	b.Send(event{typ: eventUserPrompt, text: "first"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: ""}) // carrier
	b.Send(event{typ: eventUserPrompt, text: "second"})

	require.Equal(t, []string{"first", "second"}, promptTexts(b.SnapshotPrompts(false)))

	// Read-only: the inbox is unchanged after a snapshot.
	assert.False(t, b.IsIdle())
	require.Equal(t, []string{"first", "second"}, promptTexts(b.SnapshotPrompts(false)))

	// Opt in and the carrier appears: it is deletable, so it must be
	// addressable. Nothing else changes.
	require.Equal(t, []string{"first", "", "second"}, promptTexts(b.SnapshotPrompts(true)))
}

func promptTexts(events []event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.text)
	}
	return out
}

// Ids are what the CRUD surface addresses, so they must be minted for every
// prompt, dense (a `set` in between does not burn one), and never reused
// within an inbox.
func TestInbox_PromptIDsAreDenseAndUnique(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	b.Send(event{typ: eventStudyMark, studyMark: &message.StudyMark{}})
	b.Send(event{typ: eventUserPrompt, text: "two"})

	snap := b.SnapshotPrompts(false)
	require.Len(t, snap, 2)
	assert.Equal(t, uint64(1), snap[0].id)
	assert.Equal(t, uint64(2), snap[1].id, "a control event must not consume an id")
	assert.NotZero(t, snap[0].at, "a queued message records when it was accepted")

	// Draining and re-sending does not recycle an id.
	b.TakeReadyUserPrompts()
	b.Send(event{typ: eventUserPrompt, text: "three"})
	for _, e := range b.SnapshotPrompts(false) {
		assert.NotEqual(t, uint64(1), e.id, "ids must not be reused within an inbox")
	}
}

// The epoch is what makes an id safe to mutate against: it must be stable for
// the life of one inbox and different for the next, because a client hands it
// back and the agent compares it for equality.
func TestInbox_EpochIsStablePerInboxAndFreshPerGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	epoch := b.Epoch()
	assert.NotEmpty(t, epoch)
	b.Send(event{typ: eventUserPrompt, text: "one"})
	assert.Equal(t, epoch, b.Epoch(), "the epoch must not move under a live client")

	// A new inbox is a new generation: the id space restarts, so the token
	// that qualifies it must not.
	next := NewInbox(ctx)
	assert.NotEqual(t, epoch, next.Epoch())
	next.Send(event{typ: eventUserPrompt, text: "one"})
	assert.Equal(t, uint64(1), next.SnapshotPrompts(false)[0].id,
		"ids restart per inbox, which is exactly why the epoch exists")
}

// A prompt the drain loop has lifted is no longer queued, but it is not yet a
// message either. The inbox remembers which, so a refusal can say precisely
// how late the caller was instead of collapsing everything into "unknown".
func TestInbox_LiftedThenCommitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	taken := b.TakeReadyUserPrompts()
	require.Len(t, taken, 1)
	require.Len(t, b.lifted, 1)
	assert.Empty(t, b.committed)

	b.MarkCommitted(taken)
	assert.Empty(t, b.lifted)
	require.Len(t, b.committed, 1)
	assert.Equal(t, taken[0].id, b.committed[0].id)
}

// A batch that could not be persisted goes back on the queue, so it must stop
// counting as in-flight: otherwise a delete aimed at a message that is once
// again waiting would be refused forever.
func TestInbox_PrependClearsInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	b.Send(event{typ: eventUserPrompt, text: "one"})
	taken := b.TakeReadyUserPrompts()
	require.Len(t, b.lifted, 1)

	require.True(t, b.Prepend(taken))
	assert.Empty(t, b.lifted, "a restored prompt is queued again, not in flight")
	require.Len(t, b.SnapshotPrompts(false), 1)
}

// The memory of answered ids is bounded: it only has to outlive a client's
// read→mutate round trip.
func TestInbox_CommittedRingIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	for i := 0; i < committedRing*2; i++ {
		b.Send(event{typ: eventUserPrompt, text: "x"})
		b.MarkCommitted(b.TakeReadyUserPrompts())
	}
	assert.Len(t, b.committed, committedRing)
}
