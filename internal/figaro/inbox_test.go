package figaro

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInbox_PatientDeliveredWhenIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	b.SendPatient(event{typ: eventUserPrompt, text: "hello"})

	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, eventUserPrompt, evt.typ)
	assert.Equal(t, "hello", evt.text)
}

func TestInbox_PatientHeldWhenBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// First patient makes it busy.
	b.SendPatient(event{typ: eventUserPrompt, text: "first"})
	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, "first", evt.text)

	// Second patient should be held.
	b.SendPatient(event{typ: eventUserPrompt, text: "second"})

	// Verify it's not deliverable yet.
	done := make(chan bool, 1)
	go func() {
		_, ok := b.Recv()
		done <- ok
	}()

	select {
	case <-done:
		t.Fatal("Recv should block — patient message should be held")
	case <-time.After(50 * time.Millisecond):
		// Expected — patient is waiting.
	}

	// Yield releases it.
	b.Yield()
	select {
	case ok := <-done:
		assert.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("Recv should have returned after Yield")
	}
}

func TestInbox_SelfishAlwaysDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// Make it busy (not yielded).
	b.SendPatient(event{typ: eventUserPrompt, text: "prompt"})
	b.Recv() // consume the patient

	// Now busy — selfish should still be delivered.
	ok := b.SendSelfish(event{typ: eventLLMDelta, delta: "hi"})
	require.True(t, ok)

	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, eventLLMDelta, evt.typ)
	assert.Equal(t, "hi", evt.delta)
}

func TestInbox_YieldReleasesPatient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// Start a turn.
	b.SendPatient(event{typ: eventUserPrompt, text: "first"})
	b.Recv()

	// Queue a patient while busy.
	b.SendPatient(event{typ: eventUserPrompt, text: "second"})

	// Yield should release it.
	b.Yield()

	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, "second", evt.text)
}

func TestInbox_YieldWhenEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// Start and finish a turn.
	b.SendPatient(event{typ: eventUserPrompt, text: "prompt"})
	b.Recv()
	b.Yield() // no waiting patients → yielded=true

	assert.True(t, b.IsIdle())

	// Next patient should deliver immediately (yielded).
	b.SendPatient(event{typ: eventUserPrompt, text: "next"})
	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, "next", evt.text)
}

func TestInbox_SelfishPriority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// Start a turn.
	b.SendPatient(event{typ: eventUserPrompt, text: "prompt"})
	b.Recv()

	// Queue a patient and a selfish.
	b.SendPatient(event{typ: eventUserPrompt, text: "patient"})
	b.SendSelfish(event{typ: eventLLMDelta, delta: "selfish"})

	// Selfish should come first (it's in active; patient is in waiting).
	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, eventLLMDelta, evt.typ)
	assert.Equal(t, "selfish", evt.delta)

	// Yield to release the patient.
	b.Yield()
	evt, ok = b.Recv()
	require.True(t, ok)
	assert.Equal(t, eventUserPrompt, evt.typ)
	assert.Equal(t, "patient", evt.text)
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

func TestInbox_ClosedSendReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)
	b.Close()

	ok := b.SendSelfish(event{typ: eventLLMDelta, delta: "stale"})
	assert.False(t, ok, "SendSelfish on closed inbox should return false")
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

func TestInbox_MultipleYields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInbox(ctx)

	// Start a turn, queue two patient messages.
	b.SendPatient(event{typ: eventUserPrompt, text: "first"})
	b.Recv()

	b.SendPatient(event{typ: eventUserPrompt, text: "second"})
	b.SendPatient(event{typ: eventUserPrompt, text: "third"})

	// First yield releases "second".
	b.Yield()
	evt, ok := b.Recv()
	require.True(t, ok)
	assert.Equal(t, "second", evt.text)

	// Second yield releases "third".
	b.Yield()
	evt, ok = b.Recv()
	require.True(t, ok)
	assert.Equal(t, "third", evt.text)

	// Third yield with nothing waiting → idle.
	b.Yield()
	assert.True(t, b.IsIdle())
}
