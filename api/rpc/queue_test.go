package rpc_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

func TestInterruptRequest_ZeroValueIsKeep(t *testing.T) {
	b, err := json.Marshal(rpc.InterruptRequest{})
	require.NoError(t, err)
	// A client that predates the field sends exactly this.
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

func TestQueuedPrompt_DrainedPayloadKeepsForm(t *testing.T) {
	roundTripValue(t, rpc.QueuedPrompt{
		ID:    7,
		Text:  "with state",
		State: rpc.QueueStateQueued,
		Form: &rpc.FormInput{
			Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"x"`)}, nil)),
		},
	})
}

func TestQueuedResponse_EpochAndMergedIDs(t *testing.T) {
	roundTripValue(t, rpc.QueuedResponse{
		Epoch: "0123456789abcdef",
		Prompts: []rpc.QueuedPrompt{
			{ID: 3, Text: "a\nb", State: rpc.QueueStateQueued, Merged: []uint64{4, 5}},
		},
	})
}

func TestQueuedRequest_CarriersAreOptIn(t *testing.T) {
	b, err := json.Marshal(rpc.QueuedRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(b))
	roundTripValue(t, rpc.QueuedRequest{IncludeCarriers: true})
}

func TestQueueDelete_EpochIsOnTheRequest(t *testing.T) {
	roundTripValue(t, rpc.QueueDeleteRequest{Epoch: "abcd", IDs: []uint64{2, 3}})
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

func TestQueueMethodNames(t *testing.T) {
	assert.Equal(t, "figaro.queued", rpc.MethodQueued)
	assert.Equal(t, "figaro.queue.update", rpc.MethodQueueUpdate)
	assert.Equal(t, "figaro.queue.delete", rpc.MethodQueueDelete)
}

func ptrPatch(p form.Patch) *form.Patch { return &p }
