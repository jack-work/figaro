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
