package figaro_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/rpc"
)

// drainToDone blocks until a turn.done arrives on ch.
func drainToDone(t *testing.T, ch <-chan rpc.Notification) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done")
		}
	}
}

func TestRead_Windows(t *testing.T) {
	a := newTestAgent("the answer")
	defer a.Kill()
	ch, _ := subscribeChan(a)

	a.Prompt("question")
	drainToDone(t, ch)

	// Full read: user tic (1) + assistant (2).
	full := a.Read(rpc.ReadRequest{From: 1})
	require.Len(t, full.Entries, 2)
	assert.Equal(t, uint64(1), full.Entries[0].Index)
	assert.Equal(t, uint64(2), full.Entries[1].Index)
	assert.Equal(t, uint64(2), full.Tail)
	assert.Equal(t, uint64(3), full.NextFrom)
	assert.False(t, full.Live)
	assert.Nil(t, full.Open)

	// Last: just the final message.
	last := a.Read(rpc.ReadRequest{Last: 1})
	require.Len(t, last.Entries, 1)
	assert.Equal(t, uint64(2), last.Entries[0].Index)

	// Limit caps the batch and reports the resume cursor.
	capped := a.Read(rpc.ReadRequest{From: 1, Limit: 1})
	require.Len(t, capped.Entries, 1)
	assert.Equal(t, uint64(1), capped.Entries[0].Index)
	assert.Equal(t, uint64(2), capped.NextFrom)

	// Read past the end: empty batch, resume from the same index.
	past := a.Read(rpc.ReadRequest{From: 99})
	assert.Empty(t, past.Entries)
	assert.Equal(t, uint64(99), past.NextFrom)
}
