package anthropic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// Each case asserts IR → wire == fixture, fixture → IR == expected, and the
// round trip.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	a := &Anthropic{}
	cases := []struct {
		name    string
		fixture string
		ir      message.Message
		// encoded differs from the decode fixture only for thinking blocks,
		// which the cache-miss encoder drops.
		encoded string
	}{
		{
			name:    "text_assistant",
			fixture: "text_assistant.json",
			ir: message.Message{
				Role:    message.RoleOutput,
				Content: []message.Content{message.TextContent("Hello, world!")},
			},
		},
		{
			name:    "mixed_assistant",
			fixture: "mixed_assistant.json",
			encoded: "mixed_assistant_unsigned_encode.json",
			ir: message.Message{
				Role: message.RoleOutput,
				Content: []message.Content{
					{Type: message.ContentThinking, Text: "Let me check the files."},
					message.TextContent("Listing now."),
					{
						Type: message.ContentToolInvoke, ToolCallID: "toolu_abc",
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
				Role: message.RoleInput,
				Content: []message.Content{{
					Type:       message.ContentToolResult,
					ToolCallID: "toolu_abc",
					Text:       "total 0\n-rw-r--r-- 1 me me 0 file",
				}},
			},
		},
		{
			// Arguments=nil after a WAL roundtrip must still emit "input":{}.
			name:    "empty_args_tool_call",
			fixture: "empty_args_tool_call.json",
			ir: message.Message{
				Role: message.RoleOutput,
				Content: []message.Content{{
					Type: message.ContentToolInvoke, ToolCallID: "toolu_empty",
					ToolName: "edit",
				}},
			},
		},
		{
			name:    "multi_text_user",
			fixture: "multi_text_user.json",
			ir: message.Message{
				Role: message.RoleInput,
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

			wantEncoded := wire
			if tc.encoded != "" {
				encBytes, err := os.ReadFile(filepath.Join("testdata", tc.encoded))
				require.NoError(t, err)
				wantEncoded = bytes.TrimRight(encBytes, "\n")
			}

			_, perFLT := a.projectMessages([]message.Message{tc.ir})
			require.Len(t, perFLT, 1)
			assert.Equal(t, string(wantEncoded), string(perFLT[0]), "encode parity")

			var nm nativeMessage
			require.NoError(t, json.Unmarshal(wire, &nm))
			decoded := decodeNativeMessage(nm)
			assertIRMessageEqual(t, tc.ir, decoded)

			_, perFLT2 := a.projectMessages([]message.Message{decoded})
			require.Len(t, perFLT2, 1)
			assert.Equal(t, string(wantEncoded), string(perFLT2[0]), "decode→encode round trip")
		})
	}
}

// assertIRMessageEqual skips the fields the wire does not carry.
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
		if wc.Type == message.ContentToolInvoke {
			assert.Equal(t, wc.ToolName, gc.ToolName, "block %d tool_name", i)
			wEmpty := len(wc.Arguments) == 0
			gEmpty := len(gc.Arguments) == 0
			if !(wEmpty && gEmpty) {
				wb, _ := json.Marshal(wc.Arguments)
				gb, _ := json.Marshal(gc.Arguments)
				assert.JSONEq(t, string(wb), string(gb), "block %d args", i)
			}
		}
	}
}

func TestEncodeDropsUnsignedThinking(t *testing.T) {
	a := &Anthropic{}
	_, perFLT := a.projectMessages([]message.Message{{
		Role: message.RoleOutput,
		Content: []message.Content{
			{Type: message.ContentThinking, Text: "secret deliberation"},
			message.TextContent("out loud"),
		},
	}})
	require.Len(t, perFLT, 1)
	assert.NotContains(t, string(perFLT[0]), "thinking")
	assert.Contains(t, string(perFLT[0]), "out loud")
}
