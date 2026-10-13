package rpc_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

func roundTripValue[T any](t *testing.T, v T) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var back T
	require.NoError(t, json.Unmarshal(b, &back))
	assertSameJSON(t, v, back)
}

// assertSameJSON compares by serialisation, since a struct may carry a memo.
func assertSameJSON[T any](t *testing.T, want, got T) {
	t.Helper()
	a, err := json.Marshal(want)
	require.NoError(t, err)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, string(a), string(b))
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

func roundTrip[T any](t *testing.T, fixture string, expected T) {
	t.Helper()

	fixtureData := loadFixture(t, fixture)
	var fromFixture T
	require.NoError(t, json.Unmarshal(fixtureData, &fromFixture))
	assertSameJSON(t, expected, fromFixture)

	marshaled, err := json.Marshal(expected)
	require.NoError(t, err)
	var roundTripped T
	require.NoError(t, json.Unmarshal(marshaled, &roundTripped))
	assertSameJSON(t, expected, roundTripped)
}

func TestQuaRequest(t *testing.T) {
	roundTrip(t, "qua_request.json", rpc.QuaRequest{
		Text: "explain this code",
	})
}

func TestDoneEntry(t *testing.T) {
	roundTripValue(t, rpc.DoneEntry{Reason: "stop"})
}

func TestFigaroInfoResponse(t *testing.T) {
	roundTrip(t, "figaro_info.json", rpc.FigaroInfoResponse{
		ID:           "abc123",
		State:        "active",
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-20250514",
		MessageCount: 4,
		TokensIn:     150,
		TokensOut:    42,
		CreatedAt:    1700000000000,
		LastActive:   1700000060000,
		BoundPIDs:    []int{1234, 5678},
	})
}

func TestCreateRequest(t *testing.T) {
	roundTrip(t, "create_request.json", rpc.CreateRequest{
		Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"layers":       json.RawMessage(`["anthropic"]`),
			"system.model": json.RawMessage(`"claude-sonnet-4-20250514"`),
		}, nil)),
	})
}

func TestCreateResponse(t *testing.T) {
	roundTrip(t, "create_response.json", rpc.CreateResponse{
		FigaroID: "abc123",
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: "/run/user/1000/figaro/figaros/abc123.sock",
		},
	})
}

func TestBindRequest(t *testing.T) {
	roundTrip(t, "bind_request.json", rpc.BindRequest{
		PID:      1234,
		FigaroID: "abc123",
	})
}

func TestResolveResponse_Found(t *testing.T) {
	roundTrip(t, "resolve_response_found.json", rpc.ResolveResponse{
		FigaroID: "abc123",
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: "/run/user/1000/figaro/figaros/abc123.sock",
		},
		Found: true,
	})
}

func TestResolveResponse_NotFound(t *testing.T) {
	roundTrip(t, "resolve_response_not_found.json", rpc.ResolveResponse{
		Found: false,
	})
}

func TestStatusResponse(t *testing.T) {
	roundTrip(t, "status_response.json", rpc.StatusResponse{
		Uptime:      60000,
		FigaroCount: 3,
		BoundPIDs:   2,
	})
}

func TestMethodConstants(t *testing.T) {
	assert.Equal(t, "figaro.aria", rpc.MethodAriaFrame)
	assert.Equal(t, "turn.done", rpc.MethodTurnDone)
	assert.Equal(t, "figaro.qua", rpc.MethodQua)
	assert.Equal(t, "figaro.context", rpc.MethodContext)
	assert.Equal(t, "figaro.create", rpc.MethodCreate)
	assert.Equal(t, "figaro.kill", rpc.MethodKill)
	assert.Equal(t, "pid.bind", rpc.MethodBind)
	assert.Equal(t, "pid.resolve", rpc.MethodResolve)
	assert.Equal(t, "angelus.status", rpc.MethodStatus)
}
