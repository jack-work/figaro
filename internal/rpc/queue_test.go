package rpc_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/rpc"
)

// The queue wire: identity (epoch + id), the hangup's queue disposition, and
// the per-id outcomes of a mutation. These tests pin the JSON, because the
// whole point of the epoch is that a client reads it in one call and hands it
// back in the next — a silent rename would turn that into a no-op.

func TestInterruptRequest_ZeroValueIsKeep(t *testing.T) {
	b, err := json.Marshal(rpc.InterruptRequest{})
	require.NoError(t, err)
	// A client that predates the field sends exactly this, and must keep
	// getting the old behaviour: stop the turn, leave the queue alone.
	assert.JSONEq(t, `{}`, string(b))

	var req rpc.InterruptRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
	assert.Equal(t, rpc.QueueDisposition(""), req.Queue)
	assert.NotEqual(t, rpc.QueueClear, req.Queue, "absence must never mean clear")
}

func TestInterruptRequest_ExplicitDispositions(t *testing.T) {
	for _, disp := range []rpc.QueueDisposition{rpc.QueueKeep, rpc.QueueClear} {
		roundTripValue(t, rpc.InterruptRequest{Queue: disp})
	}
	var req rpc.InterruptRequest
	require.NoError(t, json.Unmarshal([]byte(`{"queue":"clear"}`), &req))
	assert.Equal(t, rpc.QueueClear, req.Queue)
}

func TestInterruptResponse_CarriesTheQueueAsOfTheHangup(t *testing.T) {
	roundTripValue(t, rpc.InterruptResponse{
		OK:      true,
		Cleared: true,
		Epoch:   "9f3c1a5e7b2d4c60",
		Queue: []rpc.QueuedPrompt{
			{ID: 1, Text: "first", State: rpc.QueueStateQueued, At: 1700000000000},
			{ID: 2, Text: "second", State: rpc.QueueStateQueued, At: 1700000000100},
		},
	})
}

// A drained payload must carry its form input, or `figaro cut -j` is a
// lossy save of the thing it exists to preserve.
func TestQueuedPrompt_DrainedPayloadKeepsForm(t *testing.T) {
	roundTripValue(t, rpc.QueuedPrompt{
		ID:    7,
		Text:  "with state",
		State: rpc.QueueStateQueued,
		Form: &rpc.FormInput{
			Patch: &rpc.FormPatch{
				Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"x"`)},
			},
		},
	})
}

func TestQueuedResponse_EpochAndMergedIDs(t *testing.T) {
	roundTripValue(t, rpc.QueuedResponse{
		Epoch: "0123456789abcdef",
		Prompts: []rpc.QueuedPrompt{
			// The survivor of an interrupt-time coalesce names what it absorbed.
			{ID: 3, Text: "a\nb", State: rpc.QueueStateQueued, Merged: []uint64{4, 5}},
		},
	})
}

func TestQueuedRequest_CarriersAreOptIn(t *testing.T) {
	b, err := json.Marshal(rpc.QueuedRequest{})
	require.NoError(t, err)
	// The default request is byte-identical to what every existing caller
	// already sends, so the panel's output cannot change under it.
	assert.JSONEq(t, `{}`, string(b))
	roundTripValue(t, rpc.QueuedRequest{IncludeCarriers: true})
}

func TestQueueDelete_EpochIsOnTheRequest(t *testing.T) {
	roundTripValue(t, rpc.QueueDeleteRequest{Epoch: "abcd", IDs: []uint64{2, 3}})
	// The all-form names no id, so it needs no epoch.
	roundTripValue(t, rpc.QueueDeleteRequest{All: true})
}

func TestQueueDeleteResponse_PerIDOutcomes(t *testing.T) {
	roundTripValue(t, rpc.QueueDeleteResponse{
		Epoch: "abcd",
		Results: []rpc.QueueResult{
			{ID: 2, Outcome: rpc.QueueDeleted},
			{ID: 3, Outcome: rpc.QueueRejected, Reason: rpc.RejectCommitted, Detail: "committed to turn 7"},
			{ID: 4, Outcome: rpc.QueueRejected, Reason: rpc.RejectMerged, Into: 3},
		},
	})
}

func TestQueueUpdate(t *testing.T) {
	roundTripValue(t, rpc.QueueUpdateRequest{Epoch: "abcd", ID: 2, Text: "rewritten"})
	roundTripValue(t, rpc.QueueUpdateResponse{
		Epoch:  "abcd",
		Result: rpc.QueueResult{ID: 2, Outcome: rpc.QueueUpdated},
	})
}

// The reason set is CLOSED and its strings are the wire. Pinning them here
// means a rename has to be deliberate: a client switching on "committed"
// would otherwise start silently falling through to its default branch.
func TestQueueRejection_WireStringsAreStable(t *testing.T) {
	assert.Equal(t, "committing", string(rpc.RejectCommitting))
	assert.Equal(t, "committed", string(rpc.RejectCommitted))
	assert.Equal(t, "merged", string(rpc.RejectMerged))
	assert.Equal(t, "stale", string(rpc.RejectStale))
	assert.Equal(t, "unknown", string(rpc.RejectUnknown))
	assert.Equal(t, "closed", string(rpc.RejectClosed))

	assert.Equal(t, "deleted", string(rpc.QueueDeleted))
	assert.Equal(t, "updated", string(rpc.QueueUpdated))
	assert.Equal(t, "rejected", string(rpc.QueueRejected))

	assert.Equal(t, "queued", string(rpc.QueueStateQueued))
	assert.Equal(t, "committing", string(rpc.QueueStateCommitting))

	assert.Equal(t, "keep", string(rpc.QueueKeep))
	assert.Equal(t, "clear", string(rpc.QueueClear))
}

// The method names are the contract a hand-written client dials. They are
// namespaced under figaro.queue.* so the read (figaro.queued, which predates
// them) keeps its name and its shape.
func TestQueueMethodNames(t *testing.T) {
	assert.Equal(t, "figaro.queued", rpc.MethodQueued)
	assert.Equal(t, "figaro.queue.update", rpc.MethodQueueUpdate)
	assert.Equal(t, "figaro.queue.delete", rpc.MethodQueueDelete)
}
