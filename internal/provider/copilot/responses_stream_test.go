package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func responseIndex(i int) *int { return &i }

func consumeResponse(t *testing.T, state *responseStreamState, bus provider.Bus, event any) (responseObject, bool, error) {
	t.Helper()
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	var decoded responseStreamEvent
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return state.consume(decoded, raw, bus, nil)
}

func TestResponsesTerminalEventsDoNotWaitForSocketClose(t *testing.T) {
	for _, tc := range []struct {
		event, reason string
		wantErr       bool
	}{
		{"response.incomplete", "max_output_tokens", false},
		{"response.incomplete", "content_filter", true},
		{"response.incomplete", "steered", true},
		{"response.incomplete", "future_reason", true},
		{"response.failed", "", true},
		{"response.cancelled", "", true},
	} {
		t.Run(tc.event+"/"+tc.reason, func(t *testing.T) {
			server := newResponseServer(t, func(conn *websocket.Conn) {
				defer conn.Close()
				var req responseCreateRequest
				if websocket.JSON.Receive(conn, &req) != nil {
					return
				}
				_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_text.delta", "delta": "partial"})
				_ = websocket.JSON.Send(conn, map[string]any{"type": tc.event, "response": map[string]any{
					"status":             strings.TrimPrefix(tc.event, "response."),
					"incomplete_details": map[string]any{"reason": tc.reason},
					"output":             []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "partial"}}}},
				}})
				// Protocol termination, not a TCP close, must release the client.
				var next any
				_ = websocket.JSON.Receive(conn, &next)
			})
			p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			bus := &responseTestBus{}
			err := p.Send(ctx, provider.SendInput{AriaID: "terminal-probe", FigLog: newResponsesInputLog(t)}, bus)
			require.False(t, errors.Is(err, context.DeadlineExceeded), "ignored terminal event waited until deadline")
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, bus.messages, 1)
			require.Equal(t, "partial", bus.messages[0].Content[0].Text)
			if tc.wantErr {
				require.Equal(t, message.StopAborted, bus.messages[0].StopReason)
			} else {
				require.Equal(t, message.StopLength, bus.messages[0].StopReason)
			}
		})
	}
}

func TestResponsesIncompleteDoesNotRunParsableUnfinishedCall(t *testing.T) {
	state := newResponseStreamState()
	bus := &responseTestBus{}
	_, _, err := consumeResponse(t, state, bus, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
		"type": "function_call", "id": "opaque", "call_id": "call-a", "name": "write", "arguments": "",
	}})
	require.NoError(t, err)
	result, done, err := consumeResponse(t, state, bus, map[string]any{"type": "response.incomplete", "response": map[string]any{
		"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "output": []any{map[string]any{
			"type": "function_call", "call_id": "call-a", "name": "write", "status": "in_progress", "arguments": "{}",
		}},
	}})
	require.NoError(t, err)
	require.True(t, done)
	require.Empty(t, result.Output)
	require.Empty(t, bus.toolReady)
}

func TestResponsesCallCorrelationAndExactlyOnceReadiness(t *testing.T) {
	state := newResponseStreamState()
	bus := &responseTestBus{}
	for i, id := range []string{"a", "b"} {
		_, _, err := consumeResponse(t, state, bus, map[string]any{"type": "response.output_item.added", "output_index": i, "item": map[string]any{
			"type": "function_call", "id": "at-add-" + id, "call_id": id, "name": "echo", "arguments": "",
		}})
		require.NoError(t, err)
	}
	// Neither an unrecognized item ID nor a fabricated call ID gets index zero.
	for _, e := range []any{
		map[string]any{"type": "response.function_call_arguments.delta", "item_id": "unknown", "delta": "WRONG"},
		map[string]any{"type": "response.function_call_arguments.delta", "call_id": "unknown", "output_index": 0, "delta": "WRONG"},
	} {
		_, _, err := consumeResponse(t, state, bus, e)
		require.NoError(t, err)
	}
	for i, id := range []string{"a", "b"} {
		_, _, err := consumeResponse(t, state, bus, map[string]any{"type": "response.function_call_arguments.delta", "output_index": i, "item_id": "re-encrypted-" + id, "delta": `{"value":"` + id + `"}`})
		require.NoError(t, err)
		_, _, err = consumeResponse(t, state, bus, map[string]any{"type": "response.function_call_arguments.done", "output_index": i, "item_id": "another-encryption"})
		require.NoError(t, err)
		_, _, err = consumeResponse(t, state, bus, map[string]any{"type": "response.output_item.done", "output_index": i, "item": map[string]any{"type": "function_call", "call_id": id, "name": "echo", "arguments": `{"value":"` + id + `"}`}})
		require.NoError(t, err)
	}
	require.Equal(t, []string{`{"value":"a"}`, `{"value":"b"}`}, bus.toolDeltas)
	require.Len(t, bus.toolReady, 2)
	require.Equal(t, "a", bus.toolReady[0].Arguments["value"])
	require.Equal(t, "b", bus.toolReady[1].Arguments["value"])
	_, _, err := consumeResponse(t, state, bus, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": "a", "name": "echo", "arguments": `{"value":"changed"}`}})
	require.ErrorContains(t, err, "arguments changed")
	require.Len(t, bus.toolReady, 2)
}

func TestResponsesBrokenSocketRetainsNativeItemsAndReadyCalls(t *testing.T) {
	reasoning := json.RawMessage(`{"type":"reasoning","id":"opaque-item","encrypted_content":"opaque-reasoning","summary":[{"type":"summary_text","text":"thinking"}]}`)
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var req responseCreateRequest
		if websocket.JSON.Receive(conn, &req) != nil {
			return
		}
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": reasoning})
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"type": "function_call", "call_id": "a", "name": "echo", "arguments": ""}})
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.function_call_arguments.done", "output_index": 1, "arguments": `{"value":"done"}`})
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_text.delta", "output_index": 2, "delta": "tail"})
	})
	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	bus := &responseTestBus{}
	err := p.Send(context.Background(), provider.SendInput{AriaID: "broken", FigLog: newResponsesInputLog(t)}, bus)
	require.Error(t, err)
	require.Len(t, bus.messages, 1)
	require.Len(t, bus.cache, 1)
	require.JSONEq(t, string(reasoning), string(bus.cache[0].Payload[0]))
	require.Len(t, bus.messages[0].Content, 3)
	require.Equal(t, message.ContentThinking, bus.messages[0].Content[0].Type)
	require.Equal(t, message.ContentToolInvoke, bus.messages[0].Content[1].Type)
	require.Equal(t, "tail", bus.messages[0].Content[2].Text)
	require.Equal(t, message.StopAborted, bus.messages[0].StopReason)
}

func TestResponsesUnauthorizedRetryOnlyBeforeAssistantOutput(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[started], func(t *testing.T) {
			var attempts atomic.Int32
			server := newResponseServer(t, func(conn *websocket.Conn) {
				defer conn.Close()
				var req responseCreateRequest
				if websocket.JSON.Receive(conn, &req) != nil {
					return
				}
				n := attempts.Add(1)
				if n == 1 {
					if started {
						_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_text.delta", "delta": "already emitted"})
					}
					_ = websocket.JSON.Send(conn, map[string]any{"type": "error", "error": map[string]any{"code": "unauthorized", "message": "SECRET_SHOULD_NOT_APPEAR"}})
				} else {
					_ = websocket.JSON.Send(conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}}})
				}
			})
			p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
			bus := &responseTestBus{}
			err := p.Send(context.Background(), provider.SendInput{AriaID: "retry", FigLog: newResponsesInputLog(t)}, bus)
			if started {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "SECRET")
				require.Equal(t, int32(1), attempts.Load())
				require.Len(t, bus.messages, 1)
			} else {
				require.NoError(t, err)
				require.Equal(t, int32(2), attempts.Load())
			}
		})
	}
	require.False(t, isResponseUnauthorized(errors.New("a tool returned 401 or unauthorized in its payload")))
}

func TestResponsesInvalidArgumentsStayVerbatimAndNeverBecomeEmptyObject(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `"string"`, `{"content":"unfinished`, "{\"content\":\"raw\ttab\"}"} {
		call := &responseCall{ID: "call", Name: "write"}
		call.arguments.WriteString(raw)
		bus := &responseTestBus{}
		require.NoError(t, readyResponseCall(call, bus))
		require.Len(t, bus.toolReady, 1)
		got, bad := message.MalformedArgsOf(bus.toolReady[0])
		require.True(t, bad)
		require.Equal(t, raw, got)
	}
}
