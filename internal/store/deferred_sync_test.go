package store

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A stalled fsync must not hold the writer past its budget: the turn, the
// queue and every form write on the RPC path wait on it.
func TestAStalledSyncReleasesTheWriterAtItsBudget(t *testing.T) {
	gate := make(chan struct{})
	var calls atomic.Int32
	cs := &channelSyncer{key: syncKey{"a", "ir"}, sync: func(uint64) error {
		calls.Add(1)
		<-gate
		return nil
	}}

	started := time.Now()
	require.NoError(t, cs.through(1, 50*time.Millisecond))
	require.Less(t, time.Since(started), time.Second, "the writer waited for the stalled sync")

	flushed := make(chan struct{})
	go func() { cs.flush(); close(flushed) }()
	select {
	case <-flushed:
		t.Fatal("flush returned before the sync reached disk")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never returned")
	}
	require.EqualValues(t, 1, cs.done)
}

// While one sync stalls, later writes join the next one instead of each
// queueing an fsync of its own.
func TestWritesDuringAStalledSyncShareTheNextOne(t *testing.T) {
	gate := make(chan struct{})
	var targets []uint64
	cs := &channelSyncer{key: syncKey{"a", "ir"}, sync: func(idx uint64) error {
		targets = append(targets, idx)
		<-gate
		return nil
	}}
	for i := uint64(1); i <= 5; i++ {
		require.NoError(t, cs.through(i, 10*time.Millisecond))
	}
	close(gate)
	cs.flush()
	require.Equal(t, []uint64{1, 5}, targets)
	require.NoError(t, cs.through(5, time.Second), "an index already durable needs no sync")
}

// A sync that fails inside the budget is still the writer's error.
func TestAFailedSyncReachesTheWriterInsideItsBudget(t *testing.T) {
	boom := errors.New("disk said no")
	cs := &channelSyncer{key: syncKey{"a", "form"}, sync: func(uint64) error { return boom }}
	require.ErrorIs(t, cs.through(3, time.Second), boom)
	cs.flush()
	require.EqualValues(t, 0, cs.done)
}
