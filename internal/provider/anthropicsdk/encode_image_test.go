package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// renderJSON projects one IR message through the SDK encoder and returns the
// decoded wire shape, which is what actually reaches the API.
func renderJSON(t *testing.T, msg message.Message) map[string]any {
	t.Helper()
	p := &Provider{}
	snap := form.Snapshot{}
	mp, ok := p.renderMessage(msg, &snap)
	require.True(t, ok, "message rendered to nothing")
	raw, err := json.Marshal(mp)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func blocksOf(t *testing.T, m map[string]any) []map[string]any {
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

// TestSDKToolImageNestsInToolResult pins the divergence from the Responses
// shape: Anthropic accepts image blocks inside a tool_result, so the image
// stays attributed to its call instead of trailing loose in the turn.
func TestSDKToolImageNestsInToolResult(t *testing.T) {
	msg := message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.ToolResultContent("call-1", "read", "[Image: a.png]", false),
			message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
		},
	}

	rendered := renderJSON(t, msg)
	assert.Equal(t, "user", rendered["role"])

	blocks := blocksOf(t, rendered)
	require.Len(t, blocks, 1, "the image must not also appear as a top-level block")
	assert.Equal(t, "tool_result", blocks[0]["type"])
	assert.Equal(t, "call-1", blocks[0]["tool_use_id"])

	inner, ok := blocks[0]["content"].([]any)
	require.True(t, ok)
	require.Len(t, inner, 2)

	text := inner[0].(map[string]any)
	assert.Equal(t, "text", text["type"])
	assert.Equal(t, "[Image: a.png]", text["text"])

	img := inner[1].(map[string]any)
	assert.Equal(t, "image", img["type"])
	src := img["source"].(map[string]any)
	assert.Equal(t, "base64", src["type"])
	assert.Equal(t, "image/png", src["media_type"])
	assert.Equal(t, "AAAA", src["data"])
}

func TestSDKToolImageShapes(t *testing.T) {
	tests := []struct {
		name    string
		content []message.Content
		// blockTypes is the top-level block sequence on the user message.
		blockTypes []string
		// nested counts image blocks inside each tool_result, by call id.
		nested map[string]int
	}{
		{
			name: "no image renders the plain string tool_result",
			content: []message.Content{
				message.ToolResultContent("call-1", "bash", "done", false),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 0},
		},
		{
			name: "several images nest under the one call that made them",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "two frames", false),
				message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
				message.ToolImageContent("call-1", "read", "image/jpeg", "BBBB"),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 2},
		},
		{
			name: "images from two calls do not cross over",
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
			name: "an errored tool_result carries its image too",
			content: []message.Content{
				message.ToolResultContent("call-1", "shot", "capture failed", true),
				message.ToolImageContent("call-1", "shot", "image/png", "AAAA"),
			},
			blockTypes: []string{"tool_result"},
			nested:     map[string]int{"call-1": 1},
		},
		{
			name: "a user attachment stays a top-level image block",
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
			rendered := renderJSON(t, message.Message{Role: message.RoleInput, Content: tt.content})
			blocks := blocksOf(t, rendered)

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
