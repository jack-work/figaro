package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

func renderNative(t *testing.T, msg message.Message) map[string]any {
	t.Helper()
	a := &Anthropic{}
	snap := form.Snapshot{}
	nm, ok := a.renderMessage(msg, &snap)
	require.True(t, ok, "message rendered to nothing")
	raw, err := json.Marshal(nm)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func nativeBlocks(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["content"].([]any)
	require.True(t, ok, "message has no content array: %v", m)
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		bm, ok := b.(map[string]any)
		require.True(t, ok)
		out = append(out, bm)
	}
	return out
}

func TestNativeToolImageNestsInToolResult(t *testing.T) {
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call-1", "read", "[Image: a.png]", false),
			message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
		},
	}

	rendered := renderNative(t, msg)
	assert.Equal(t, "user", rendered["role"])

	blocks := nativeBlocks(t, rendered)
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_result", blocks[0]["type"])
	assert.Equal(t, "call-1", blocks[0]["tool_use_id"])

	inner, ok := blocks[0]["content"].([]any)
	require.True(t, ok)
	require.Len(t, inner, 2)
	assert.Equal(t, "text", inner[0].(map[string]any)["type"])

	img := inner[1].(map[string]any)
	assert.Equal(t, "image", img["type"])
	src := img["source"].(map[string]any)
	assert.Equal(t, "base64", src["type"])
	assert.Equal(t, "image/png", src["media_type"])
	assert.Equal(t, "AAAA", src["data"])
}

func TestNativeToolImageShapes(t *testing.T) {
	tests := []struct {
		name       string
		content    []message.Content
		blockTypes []string
		nested     map[string]int
	}{
		{
			name: "no image is the shape it always was",
			content: []message.Content{
				message.ToolResultContent("call-1", "bash", "done", false),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 0},
		},
		{
			name: "several images nest under one call",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "two frames", false),
				message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
				message.ToolImageContent("call-1", "read", "image/jpeg", "BBBB"),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 2},
		},
		{
			name: "two calls keep their own images",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "one", false),
				message.ToolResultContent("call-2", "shot", "two", false),
				message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
				message.ToolImageContent("call-2", "shot", "image/gif", "BBBB"),
			},
			blockTypes: []string{"tool_result", "tool_result"},
			nested:     map[string]int{"call-1": 1, "call-2": 1},
		},
		{
			name: "an errored tool_result carries its image",
			content: []message.Content{
				message.ToolResultContent("call-1", "shot", "capture failed", true),
				message.ToolImageContent("call-1", "shot", "image/png", "AAAA"),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 1},
		},
		{
			name: "a user attachment stays top level",
			content: []message.Content{
				message.TextContent("what is this"),
				message.ImageContent("image/png", "AAAA"),
			},
			blockTypes: []string{"text", "image"},
		},
		{
			name: "an image naming a call with no result is not swallowed",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "one", false),
				message.ToolImageContent("call-9", "ghost", "image/png", "AAAA"),
			},
			blockTypes: []string{"tool_result", "image"},
			nested:     map[string]int{"call-1": 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := renderNative(t, message.Message{Role: message.RoleInput, Content: tt.content})
			blocks := nativeBlocks(t, rendered)

			var gotTypes []string
			for _, b := range blocks {
				gotTypes = append(gotTypes, b["type"].(string))
			}
			assert.Equal(t, tt.blockTypes, gotTypes)

			for id, want := range tt.nested {
				var found bool
				for _, b := range blocks {
					if b["type"] != "tool_result" || b["tool_use_id"] != id {
						continue
					}
					found = true
					images := 0
					if inner, ok := b["content"].([]any); ok {
						for _, c := range inner {
							if c.(map[string]any)["type"] == "image" {
								images++
							}
						}
					}
					assert.Equal(t, want, images, "nested images for %s", id)
				}
				require.True(t, found, "no tool_result for %s", id)
			}
		})
	}
}

// A picture sent with a prompt is a top-level image block on the user turn,
// ahead of the text, with no tool_result around it.
func TestNativePromptImageIsAnImageBlockBeforeTheText(t *testing.T) {
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ImageContent("image/jpeg", "/9j/AAAA"),
			message.SenderText("payup", "what is the total?"),
		},
	}

	blocks := nativeBlocks(t, renderNative(t, msg))
	require.Len(t, blocks, 2)
	assert.Equal(t, "image", blocks[0]["type"])
	src, ok := blocks[0]["source"].(map[string]any)
	require.True(t, ok, "image block has no source: %v", blocks[0])
	assert.Equal(t, "base64", src["type"])
	assert.Equal(t, "image/jpeg", src["media_type"])
	assert.Equal(t, "/9j/AAAA", src["data"])
	assert.Equal(t, "text", blocks[1]["type"])
	assert.Contains(t, blocks[1]["text"], "what is the total?")
}
