package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// ONE INVOKE IS CLOSED ONCE, however many doors are open over the log.
//
// Gluck, 0.28.2: "messages.682.content.2: unexpected tool_use_id found in
// tool_result blocks". The tool_use was present -- the CLOSING NOTICE was
// written TWICE, and the API refuses the second because the invoke was
// already answered by the first.
//
//	690 output tool_invoke  X
//	691 input  tool_result  X  "tool call closed without a result (interrupt…"
//	692 input  tool_result  X  "tool call closed without a result (interrupt…"
//
// OpenFigIR mints a NEW door per call, each with its own in-memory idea of
// what is open. finishTurn's CloseOpenToolCalls opened a second door, closed
// the call, and the agent's own door never learned -- so its next append
// closed it again. Two copies of one invariant, disagreeing.
func TestOneInvokeIsClosedOnceAcrossDoors(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)

	agentDoor, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = agentDoor.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput,
		Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "X", ToolName: "bash",
		}},
	}})
	require.NoError(t, err)

	// The interrupt path closes it through a door of its own.
	n, err := be.CloseOpenToolCalls(aria)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// The agent carries on through the door it already had.
	_, err = agentDoor.Append(Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("carry on")},
	}})
	require.NoError(t, err)

	closes := 0
	for _, e := range agentDoor.Read() {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID == "X" {
				closes++
			}
		}
	}
	require.Equal(t, 1, closes,
		"the call was closed %d times; every result after the first has no tool_use "+
			"left to pair with, and the provider refuses the whole history", closes)
}
