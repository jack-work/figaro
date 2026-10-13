package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// One invoke is closed once, however many doors are open over the log: a
// second closing notice at one tool_use_id is refused by the provider.
// OpenFigIR mints a door per call, so what is open must be read from the
// log, not remembered.
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
