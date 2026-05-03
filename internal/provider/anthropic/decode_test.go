package anthropic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// Each case asserts byte-for-byte parity in three directions:
//   IR → wire == fixture
//   fixture → IR == expected
//   fixture → IR → wire == fixture
func TestEncodeDecodeRoundTrip(t *testing.T) {
	a := &Anthropic{}
	cases := []struct {
		name    string
		fixture string
		ir      message.Message
	}{
		{
			name:    "text_assistant",
			fixture: "text_assistant.json",
			ir: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.Content{message.TextContent("Hello, world!")},
			},
		},
		{
			name:    "mixed_assistant",
			fixture: "mixed_assistant.json",
			ir: message.Message{
				Role: message.RoleAssistant,
				Content: []message.Content{
					{Type: message.ContentThinking, Text: "Let me check the files."},
					message.TextContent("Listing now."),
					{
						Type: message.ContentToolCall, ToolCallID: "toolu_abc",
						ToolName:  "bash",
						Arguments: map[string]interface{}{"command": "ls -la"},
					},
				},
			},
		},
		{
			name:    "tool_result_user",
			fixture: "tool_result_user.json",
			ir: message.Message{
				Role: message.RoleUser,
				Content: []message.Content{{
					Type:       message.ContentToolResult,
					ToolCallID: "toolu_abc",
					Text:       "total 0\n-rw-r--r-- 1 me me 0 file",
				}},
			},
		},
		{
			name:    "multi_text_user",
			fixture: "multi_text_user.json",
			ir: message.Message{
				Role: message.RoleUser,
				Content: []message.Content{
					message.TextContent("first"),
					message.TextContent("second"),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wireBytes, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			require.NoError(t, err)
			wire := bytes.TrimRight(wireBytes, "\n")

			// Encode parity: IR → wire == fixture.
			_, perFLT := a.projectMessages([]message.Message{tc.ir})
			require.Len(t, perFLT, 1)
			assert.Equal(t, string(wire), string(perFLT[0]), "encode parity")

			// Decode parity: fixture → IR == expected.
			decoded, err := a.Decode([]json.RawMessage{wire})
			require.NoError(t, err)
			require.Len(t, decoded, 1)
			assertIRMessageEqual(t, tc.ir, decoded[0])

			// Round trip: fixture → IR → wire == fixture.
			_, perFLT2 := a.projectMessages(decoded)
			require.Len(t, perFLT2, 1)
			assert.Equal(t, string(wire), string(perFLT2[0]), "decode→encode round trip")
		})
	}
}

// assertIRMessageEqual compares two IR Messages for the fields the
// wire round-trip preserves. Skips Timestamp / LogicalTime / ToolName
// on tool_result (encoder doesn't put it on the wire, so decode can't
// recover it).
func assertIRMessageEqual(t *testing.T, want, got message.Message) {
	t.Helper()
	assert.Equal(t, want.Role, got.Role)
	require.Equal(t, len(want.Content), len(got.Content), "content length")
	for i := range want.Content {
		wc, gc := want.Content[i], got.Content[i]
		assert.Equal(t, wc.Type, gc.Type, "block %d type", i)
		assert.Equal(t, wc.Text, gc.Text, "block %d text", i)
		assert.Equal(t, wc.ToolCallID, gc.ToolCallID, "block %d tool_call_id", i)
		assert.Equal(t, wc.IsError, gc.IsError, "block %d is_error", i)
		if wc.Type == message.ContentToolCall {
			assert.Equal(t, wc.ToolName, gc.ToolName, "block %d tool_name", i)
			wb, _ := json.Marshal(wc.Arguments)
			gb, _ := json.Marshal(gc.Arguments)
			assert.JSONEq(t, string(wb), string(gb), "block %d args", i)
		}
	}
}
