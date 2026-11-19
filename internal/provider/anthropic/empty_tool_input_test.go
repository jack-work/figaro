package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// figaro#141: a call to a tool with no arguments streams one input_json_delta
// with partial_json "". The call must land, not vanish into an empty
// completion with stop_reason=tool_use.
func TestToolUseWithEmptyPartialJSONLands(t *testing.T) {
	evTool := "event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"noargs","input":{}}}` +
		"\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}` +
		"\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}`
	bus, err := sendSSE(t, sseBody(evStart, evTool, evDelta("tool_use"), evStop))
	require.NoError(t, err)
	require.Len(t, bus.msgs, 1)
	require.Len(t, bus.msgs[0].Content, 1)
	c := bus.msgs[0].Content[0]
	assert.Equal(t, message.ContentToolInvoke, c.Type)
	assert.Equal(t, "toolu_1", c.ToolCallID)
	assert.Empty(t, c.Arguments)
	assert.Equal(t, message.StopToolInvoke, bus.msgs[0].StopReason)
}
