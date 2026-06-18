package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

// mustParams marshals a frame body to json.RawMessage for handle().
func mustParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func newTestSink() (*plainSink, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &plainSink{out: out, printed: map[int]int{}, doneCh: make(chan struct{}, 1)}, out
}

func TestPlainSink_AssistantStreamsThenSeals(t *testing.T) {
	s, out := newTestSink()

	// Full-mode open frames carry the whole message each time; the sink
	// must emit only the new suffix, never re-printing.
	s.handle(rpc.MethodLogOpen, mustParams(t, rpc.OpenEntry{
		Index: 2, Version: 1, Open: true,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "Hel"}}},
	}))
	s.handle(rpc.MethodLogOpen, mustParams(t, rpc.OpenEntry{
		Index: 2, Version: 2, Open: true,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "Hello, wor"}}},
	}))
	s.handle(rpc.MethodLogEntry, mustParams(t, rpc.LogEntry{
		Index: 2,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "Hello, world"}}},
	}))

	assert.Equal(t, "Hello, world", out.String())
}

func TestPlainSink_SkipsUserPromptTic(t *testing.T) {
	s, out := newTestSink()

	// A user prompt tic must not be echoed back.
	s.handle(rpc.MethodLogEntry, mustParams(t, rpc.LogEntry{
		Index:   1,
		Message: message.Message{Role: message.RoleUser, Content: []message.Content{{Type: message.ContentText, Text: "what is 2+2?"}}},
	}))
	// The assistant reply is rendered.
	s.handle(rpc.MethodLogEntry, mustParams(t, rpc.LogEntry{
		Index:   2,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "4"}}},
	}))

	assert.Equal(t, "4", out.String())
}

func TestPlainSink_ToolResultOutput(t *testing.T) {
	s, out := newTestSink()

	// Tool execution streams as an open tool_result message (role user).
	s.handle(rpc.MethodLogOpen, mustParams(t, rpc.OpenEntry{
		Index: 3, Version: 1, Open: true,
		Message: message.Message{Role: message.RoleUser, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: "tc_1", ToolName: "bash", Text: "line1\n"},
		}},
	}))
	s.handle(rpc.MethodLogEntry, mustParams(t, rpc.LogEntry{
		Index: 3,
		Message: message.Message{Role: message.RoleUser, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: "tc_1", ToolName: "bash", Text: "line1\nline2\n"},
		}},
	}))

	assert.Equal(t, "line1\nline2\n", out.String())
}

func TestPlainSink_AbortDropsOpenTail(t *testing.T) {
	s, out := newTestSink()

	s.handle(rpc.MethodLogOpen, mustParams(t, rpc.OpenEntry{
		Index: 2, Version: 1, Open: true,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "partial"}}},
	}))
	s.handle(rpc.MethodLogAbort, mustParams(t, rpc.AbortEntry{Index: 2, Reason: "user_interrupt"}))
	// A fresh message reuses the burned index; its content renders cleanly.
	s.handle(rpc.MethodLogEntry, mustParams(t, rpc.LogEntry{
		Index:   2,
		Message: message.Message{Role: message.RoleAssistant, Content: []message.Content{{Type: message.ContentText, Text: "fresh"}}},
	}))

	assert.Equal(t, "partialfresh", out.String())
}

func TestPlainSink_TurnDoneSignals(t *testing.T) {
	s, _ := newTestSink()

	s.handle(rpc.MethodTurnDone, mustParams(t, rpc.DoneEntry{Reason: "stop"}))
	select {
	case <-s.doneCh:
	default:
		t.Fatal("turn.done did not signal doneCh")
	}
	assert.False(t, s.sawError)
}

func TestPlainSink_TurnDoneError(t *testing.T) {
	s, _ := newTestSink()
	s.handle(rpc.MethodTurnDone, mustParams(t, rpc.DoneEntry{Reason: "error: boom"}))
	assert.True(t, s.sawError)
}
