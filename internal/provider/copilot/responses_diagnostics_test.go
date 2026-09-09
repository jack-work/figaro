package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	responsews "github.com/coder/websocket"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/wirelog"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

type responseObservedBus struct {
	responseTestBus
	ready func()
}

func (b *responseObservedBus) PushToolReady(c message.Content) {
	b.responseTestBus.PushToolReady(c)
	if b.ready != nil {
		b.ready()
	}
}

func TestResponsesDiagnosticsFollowActualFramesWithoutPayloads(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	requestBytes := make(chan int, 1)
	const argument = `{"value":"PRIVATE_ARGUMENT_CONTENT"}`
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var raw json.RawMessage
		if websocket.JSON.Receive(conn, &raw) != nil {
			return
		}
		requestBytes <- len(raw)
		for _, event := range []any{
			map[string]any{"type": "response.created"},
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
				"type": "function_call", "id": "PRIVATE_ITEM_ID", "call_id": "PRIVATE_CALL_ID", "name": "echo", "arguments": "",
			}},
			map[string]any{"type": "response.function_call_arguments.delta", "item_id": "stray", "delta": "PRIVATE_UNMATCHED_CONTENT"},
			map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "rewritten", "delta": argument},
			map[string]any{"type": "PRIVATE_UNKNOWN_EVENT_TYPE", "text": "PRIVATE_PAYLOAD"},
			map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "arguments": argument},
			map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{
				"type": "function_call", "call_id": "PRIVATE_CALL_ID", "name": "echo", "arguments": argument,
			}}}},
		} {
			if websocket.JSON.Send(conn, event) != nil {
				return
			}
		}
	})
	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	var live *wirelog.InFlight
	bus := &responseObservedBus{ready: func() {
		for _, row := range wirelog.Outstanding() {
			if row.Aria == "diagnostic-provider-probe" {
				copy := row
				live = &copy
			}
		}
	}}
	err := p.Send(context.Background(), provider.SendInput{AriaID: "diagnostic-provider-probe", FigLog: newResponsesInputLog(t)}, bus)
	require.NoError(t, err)
	require.NotNil(t, live, "actual provider attempt was never registered in-flight")
	require.NotNil(t, live.Stream)
	require.Equal(t, "WS", live.Method)
	require.Equal(t, int64(2), live.Stream.ArgumentDeltas)
	require.Equal(t, int64(1), live.Stream.ArgumentUnmatched)
	require.Equal(t, int64(1), live.Stream.UnknownEvents)
	require.Positive(t, live.Stream.FirstToolMS)
	require.GreaterOrEqual(t, live.Stream.FirstArgumentMS, live.Stream.FirstToolMS)
	for _, row := range wirelog.Outstanding() {
		require.NotEqual(t, "diagnostic-provider-probe", row.Aria)
	}
	var record map[string]any
	count := 0
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal(line, &entry))
		if entry["msg"] == wirelog.RoundLog {
			record = entry
			count++
		}
	}
	require.Equal(t, 1, count, "one record per actual attempt")
	require.Equal(t, "completed", record["stream_status"])
	require.Equal(t, float64(7), record["stream_events"])
	require.Equal(t, float64(1), record["stream_unknown_events"])
	require.Equal(t, float64(<-requestBytes), record["req_bytes"])
	require.NotContains(t, logs.String(), "PRIVATE_")
	require.NotContains(t, logs.String(), "test-token")
}

func TestResponsesDiagnosticsIncludeFailedDial(t *testing.T) {
	p := newResponsesTestProvider(newResponseServer(t, func(conn *websocket.Conn) { conn.Close() }), store.NewMemLog[[]json.RawMessage]())
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	p.dial = func(context.Context, string, http.Header) (*responsews.Conn, error) {
		return nil, context.DeadlineExceeded
	}
	err := p.Send(context.Background(), provider.SendInput{AriaID: "failed-dial-probe", FigLog: newResponsesInputLog(t)}, &responseTestBus{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, logs.String(), "failed-dial-probe")
	require.Contains(t, logs.String(), "deadline_exceeded")
	for _, row := range wirelog.Outstanding() {
		require.NotEqual(t, "failed-dial-probe", row.Aria)
	}
}
