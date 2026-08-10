package copilot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// decodeItems unmarshals the encoded Responses input items so a test can
// assert on shape rather than on substring luck.
func decodeItems(t *testing.T, raws []json.RawMessage) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(raws))
	for _, raw := range raws {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	return out
}

func itemContent(t *testing.T, item map[string]any) []map[string]any {
	t.Helper()
	raw, ok := item["content"].([]any)
	require.True(t, ok, "item has no content array: %v", item)
	out := make([]map[string]any, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out
}

// TestResponsesToolImageTrailsTheOutput pins the shape this provider must
// use: the Responses function_call_output takes a plain string, so a tool's
// image cannot ride inside it. It follows in a user message instead, after
// every function_call_output, captioned with the call it came from.
func TestResponsesToolImageTrailsTheOutput(t *testing.T) {
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call-1", "read", "[Image: a.png]", false),
			message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
		},
	}

	input, err := encodeResponseMessage(msg, nil, form.Snapshot{}, nil)
	require.NoError(t, err)
	items := decodeItems(t, input)
	require.Len(t, items, 2)

	assert.Equal(t, "function_call_output", items[0]["type"])
	assert.Equal(t, "call-1", items[0]["call_id"])
	assert.Equal(t, "[Image: a.png]", items[0]["output"])
	assert.NotContains(t, items[0], "content",
		"the image must not be smuggled into the function_call_output")

	assert.Equal(t, "user", items[1]["role"])
	content := itemContent(t, items[1])
	require.Len(t, content, 2)
	assert.Equal(t, "input_text", content[0]["type"])
	assert.Contains(t, content[0]["text"], "read")
	assert.Contains(t, content[0]["text"], "call-1")
	assert.Equal(t, "input_image", content[1]["type"])
	assert.Equal(t, "data:image/png;base64,AAAA", content[1]["image_url"])
}

func TestResponsesToolImageShapes(t *testing.T) {
	tests := []struct {
		name       string
		content    []message.Content
		wantItems  int
		wantImages int
		assertFn   func(t *testing.T, items []map[string]any)
	}{
		{
			name: "no image leaves a lone function_call_output",
			content: []message.Content{
				message.ToolResultContent("call-1", "bash", "done", false),
			},
			wantItems: 1,
			assertFn: func(t *testing.T, items []map[string]any) {
				assert.Equal(t, "function_call_output", items[0]["type"])
			},
		},
		{
			name: "several images from several calls each keep a caption",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "one", false),
				message.ToolResultContent("call-2", "shot", "two", false),
				message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
				message.ToolImageContent("call-2", "shot", "image/gif", "BBBB"),
			},
			wantItems:  3,
			wantImages: 2,
			assertFn: func(t *testing.T, items []map[string]any) {
				assert.Equal(t, "function_call_output", items[0]["type"])
				assert.Equal(t, "function_call_output", items[1]["type"])
				assert.Equal(t, "user", items[2]["role"])
				content := itemContent(t, items[2])
				require.Len(t, content, 4)
				assert.Contains(t, content[0]["text"], "call-1")
				assert.Equal(t, "input_image", content[1]["type"])
				assert.Contains(t, content[2]["text"], "call-2")
				assert.Equal(t, "input_image", content[3]["type"])
			},
		},
		{
			name: "an image on a failed call still reaches the model",
			content: []message.Content{
				message.ToolResultContent("call-1", "shot", "capture failed", true),
				message.ToolImageContent("call-1", "shot", "image/png", "AAAA"),
			},
			wantItems:  2,
			wantImages: 1,
		},
		{
			name: "a user attachment gets no caption",
			content: []message.Content{
				message.TextContent("what is this"),
				message.ImageContent("image/png", "AAAA"),
			},
			wantItems:  1,
			wantImages: 1,
			assertFn: func(t *testing.T, items []map[string]any) {
				content := itemContent(t, items[0])
				require.Len(t, content, 2)
				assert.Equal(t, "what is this", content[0]["text"])
				assert.Equal(t, "input_image", content[1]["type"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := message.Message{Role: message.RoleInput, Content: tt.content}
			input, err := encodeResponseMessage(msg, nil, form.Snapshot{}, nil)
			require.NoError(t, err)

			items := decodeItems(t, input)
			require.Len(t, items, tt.wantItems)

			images := 0
			for _, item := range items {
				if item["role"] != "user" {
					continue
				}
				for _, c := range itemContent(t, item) {
					if c["type"] == "input_image" {
						images++
					}
				}
			}
			assert.Equal(t, tt.wantImages, images)

			if tt.assertFn != nil {
				tt.assertFn(t, items)
			}
		})
	}
}
