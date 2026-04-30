package tokens

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/stretchr/testify/assert"
)

func TestContextSize_NilBlock(t *testing.T) {
	tokens, exact := ContextSize(nil)
	assert.Equal(t, 0, tokens)
	assert.True(t, exact)
}

func TestContextSize_EmptyBlock(t *testing.T) {
	tokens, exact := ContextSize(&message.Block{})
	assert.Equal(t, 0, tokens)
	assert.True(t, exact)
}

func TestContextSize_WatermarkIsLeaf(t *testing.T) {
	block := &message.Block{
		Messages: []message.Message{
			{Role: message.RoleUser, Content: []message.Content{
				message.TextContent("hello"),
			}},
			{Role: message.RoleAssistant, Content: []message.Content{
				message.TextContent("hi there"),
			}, Usage: &message.Usage{
				InputTokens:  500,
				OutputTokens: 50,
			}},
		},
	}

	tokens, exact := ContextSize(block)
	assert.Equal(t, 550, tokens)
	assert.True(t, exact)
}

func TestContextSize_MessagesAfterWatermark(t *testing.T) {
	block := &message.Block{
		Messages: []message.Message{
			{Role: message.RoleUser, Content: []message.Content{
				message.TextContent("hello"),
			}},
			{Role: message.RoleAssistant, Content: []message.Content{
				message.TextContent("hi there"),
			}, Usage: &message.Usage{
				InputTokens:  500,
				OutputTokens: 50,
			}},
			{Role: message.RoleUser, Content: []message.Content{
				// 40 chars → ceil(40/4) = 10 tokens
				message.TextContent("now do something else for me please ok?!"),
			}},
		},
	}

	tokens, exact := ContextSize(block)
	assert.Equal(t, 560, tokens)
	assert.False(t, exact)
}

func TestContextSize_NoUsage(t *testing.T) {
	block := &message.Block{
		Messages: []message.Message{
			{Role: message.RoleUser, Content: []message.Content{
				// 12 chars → ceil(12/4) = 3
				message.TextContent("hello world!"),
			}},
			{Role: message.RoleAssistant, Content: []message.Content{
				// 8 chars → ceil(8/4) = 2
				message.TextContent("hi there"),
			}},
		},
	}

	tokens, exact := ContextSize(block)
	assert.Equal(t, 5, tokens)
	assert.False(t, exact)
}

func TestEstimateMessage_Text(t *testing.T) {
	m := message.Message{Content: []message.Content{
		message.TextContent("abcdefgh"), // 8 chars → 2
	}}
	assert.Equal(t, 2, EstimateMessage(m))
}

func TestEstimateMessage_Thinking(t *testing.T) {
	m := message.Message{Content: []message.Content{
		{Type: message.ContentThinking, Text: "abcdefgh"}, // 8 chars → 2
	}}
	assert.Equal(t, 2, EstimateMessage(m))
}

func TestEstimateMessage_Image(t *testing.T) {
	m := message.Message{Content: []message.Content{
		{Type: message.ContentImage, MimeType: "image/png", Data: "..."},
	}}
	assert.Equal(t, 1200, EstimateMessage(m))
}

func TestEstimateMessage_ToolCall(t *testing.T) {
	m := message.Message{Content: []message.Content{
		{
			Type:      message.ContentToolCall,
			ToolName:  "bash",
			Arguments: map[string]interface{}{"command": "ls -la"},
		},
	}}
	// "bash" = 4 chars, {"command":"ls -la"} = 20 chars → 24 chars → 6
	tokens := EstimateMessage(m)
	assert.Greater(t, tokens, 0)
}

func TestEstimateMessage_Empty(t *testing.T) {
	m := message.Message{}
	assert.Equal(t, 0, EstimateMessage(m))
}

func TestEstimateMessage_CeilRounding(t *testing.T) {
	// 5 chars → ceil(5/4) = 2
	m := message.Message{Content: []message.Content{
		message.TextContent("abcde"),
	}}
	assert.Equal(t, 2, EstimateMessage(m))
}
