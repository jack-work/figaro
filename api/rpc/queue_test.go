package rpc_test

import (
	"encoding/json"
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

func TestQueuedRequest_CarriersAreOptIn(t *testing.T) {
	b, err := json.Marshal(rpc.QueuedRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(b))
	roundTripValue(t, rpc.QueuedRequest{IncludeCarriers: true})
}
