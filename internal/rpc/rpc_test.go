package rpc_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/rpc"
)

// loadFixture reads a testdata file.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

// roundTrip marshals v to JSON, then unmarshals into a new instance of the same type,
// and verifies equality. Also verifies the JSON matches the fixture.
func roundTrip[T any](t *testing.T, fixture string, expected T) {
	t.Helper()

	// 1. Unmarshal fixture into type.
	fixtureData := loadFixture(t, fixture)
	var fromFixture T
	require.NoError(t, json.Unmarshal(fixtureData, &fromFixture))
	assert.Equal(t, expected, fromFixture, "fixture should unmarshal to expected value")

	// 2. Marshal expected, unmarshal back, verify round-trip.
	marshaled, err := json.Marshal(expected)
	require.NoError(t, err)
	var roundTripped T
	require.NoError(t, json.Unmarshal(marshaled, &roundTripped))
	assert.Equal(t, expected, roundTripped, "round-trip should preserve value")
}

// --- Figaro socket types ---

func TestQuaRequest(t *testing.T) {
	roundTrip(t, "qua_request.json", rpc.QuaRequest{
		Text: "explain this code",
	})
}

func TestDeltaParams(t *testing.T) {
	roundTrip(t, "delta_notification.json", rpc.DeltaParams{
		Text:        "Hello",
		ContentType: "text",
	})
}

func TestToolInvokeStartParams(t *testing.T) {
	roundTrip(t, "tool_invoke_start.json", rpc.ToolInvokeStartParams{
		ToolCallID: "toolu_01ABC",
		ToolName:   "edit",
	})
}

func TestToolInvokeDeltaParams(t *testing.T) {
	roundTrip(t, "tool_invoke_delta.json", rpc.ToolInvokeDeltaParams{
		ToolCallID:  "toolu_01ABC",
		PartialJSON: `{"path":"x.go"`,
	})
}

func TestToolInvokeReadyParams(t *testing.T) {
	roundTrip(t, "tool_invoke_ready.json", rpc.ToolInvokeReadyParams{
		ToolCallID: "toolu_01ABC",
		ToolName:   "edit",
		Arguments:  map[string]interface{}{"path": "x.go"},
	})
}

func TestMessageEndParams(t *testing.T) {
	roundTrip(t, "message_end.json", rpc.MessageEndParams{
		StopReason: "tool_use",
	})
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

// --- Angelus socket types ---

func TestCreateRequest(t *testing.T) {
	roundTrip(t, "create_request.json", rpc.CreateRequest{
		Loadout: "anthropic",
		Patch: &rpc.ChalkboardPatch{
			Set: map[string]json.RawMessage{
				"system.model": json.RawMessage(`"claude-sonnet-4-20250514"`),
			},
		},
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

// --- Method constants ---

func TestMethodConstants(t *testing.T) {
	// Verify method names follow naming convention.
	assert.Equal(t, "stream.delta", rpc.MethodDelta)
	assert.Equal(t, "stream.tool_invoke_start", rpc.MethodToolInvokeStart)
	assert.Equal(t, "stream.tool_invoke_delta", rpc.MethodToolInvokeDelta)
	assert.Equal(t, "stream.tool_invoke_ready", rpc.MethodToolInvokeReady)
	assert.Equal(t, "stream.message_end", rpc.MethodMessageEnd)
	assert.Equal(t, "stream.done", rpc.MethodDone)
	assert.Equal(t, "figaro.qua", rpc.MethodQua)
	assert.Equal(t, "figaro.context", rpc.MethodContext)
	assert.Equal(t, "figaro.create", rpc.MethodCreate)
	assert.Equal(t, "figaro.kill", rpc.MethodKill)
	assert.Equal(t, "pid.bind", rpc.MethodBind)
	assert.Equal(t, "pid.resolve", rpc.MethodResolve)
	assert.Equal(t, "angelus.status", rpc.MethodStatus)
}
