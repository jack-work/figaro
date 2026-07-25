package figaro

import (
	"context"
	"testing"
	"time"

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

func TestInbox_ReadyForksPreserveFIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventSet})
	b.Send(event{typ: eventFork})

	assert.Empty(t, b.TakeReadyForks())
	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, eventSet, evt.typ)
	require.Len(t, b.TakeReadyForks(), 1)
	assert.True(t, b.IsIdle())
}

func TestInbox_RemovingPromptRearmsReadyFork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt})
	b.Send(event{typ: eventFork})

	<-b.Wake()
	assert.Empty(t, b.TakeReadyForks())
	require.Len(t, b.TakeReadyUserPrompts(), 1)
	select {
	case <-b.Wake():
	case <-time.After(time.Second):
		t.Fatal("ready fork was not re-armed")
	}
	require.Len(t, b.TakeReadyForks(), 1)
}

func TestInbox_TakeReadySetContiguousPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "p"})
	b.Send(event{typ: eventSet})

	// A set behind a prompt is not taken until the prompt clears — the
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
	b.Send(event{typ: eventSet})
	b.Send(event{typ: eventUserPrompt, text: "mid"})
	b.Send(event{typ: eventSet})

	require.Len(t, b.TakeReadySet(), 1) // leading set
	assert.Empty(t, b.TakeReadySet())   // now blocked by the prompt
	p := b.TakeReadyUserPrompts()
	require.Len(t, p, 1)
	assert.Equal(t, "mid", p[0].text)
	require.Len(t, b.TakeReadySet(), 1) // trailing set
	assert.True(t, b.IsIdle())
}

func TestInbox_PromptForkPromptBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)
	b.Send(event{typ: eventUserPrompt, text: "first"})
	b.Send(event{typ: eventFork})
	b.Send(event{typ: eventUserPrompt, text: "second"})

	first := b.TakeReadyUserPrompts()
	require.Len(t, first, 1)
	assert.Equal(t, "first", first[0].text)
	require.Len(t, b.TakeReadyForks(), 1)
	second := b.TakeReadyUserPrompts()
	require.Len(t, second, 1)
	assert.Equal(t, "second", second[0].text)
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

func TestInbox_SnapshotUserPromptsFIFOAndReadOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewInbox(ctx)

	// A pure-chalkboard prompt (empty text) is a carrier, not a message —
	// the snapshot omits it. Non-prompt events (Set, Fork) are also skipped.
	b.Send(event{typ: eventUserPrompt, text: "first"})
	b.Send(event{typ: eventSet})
	b.Send(event{typ: eventUserPrompt, text: ""}) // carrier
	b.Send(event{typ: eventUserPrompt, text: "second"})
	b.Send(event{typ: eventFork})

	snap := b.SnapshotUserPrompts()
	require.Equal(t, []string{"first", "second"}, snap)

	// Read-only: the inbox is unchanged after a snapshot.
	assert.False(t, b.IsIdle())
	snap2 := b.SnapshotUserPrompts()
	require.Equal(t, snap, snap2)
}
