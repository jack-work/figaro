package tokens

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/stretchr/testify/assert"
)

func TestContextSize_NilSlice(t *testing.T) {
	tokens, exact := ContextSize(nil)
	assert.Equal(t, 0, tokens)
	assert.True(t, exact)
}

func TestContextSize_EmptySlice(t *testing.T) {
	tokens, exact := ContextSize([]message.Message{})
	assert.Equal(t, 0, tokens)
	assert.True(t, exact)
}

func TestContextSize_WatermarkIsLeaf(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{
			message.TextContent("hello"),
		}},
		{Role: message.RoleOutput, Content: []message.Content{
			message.TextContent("hi there"),
		}, Usage: &message.Usage{
			InputTokens:  500,
			OutputTokens: 50,
		}},
	}

	tokens, exact := ContextSize(msgs)
	assert.Equal(t, 550, tokens)
	assert.True(t, exact)
}

func TestContextSize_MessagesAfterWatermark(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{
			message.TextContent("hello"),
		}},
		{Role: message.RoleOutput, Content: []message.Content{
			message.TextContent("hi there"),
		}, Usage: &message.Usage{
			InputTokens:  500,
			OutputTokens: 50,
		}},
		{Role: message.RoleInput, Content: []message.Content{
			// 40 chars → ceil(40/4) = 10 tokens
			message.TextContent("now do something else for me please ok?!"),
		}},
	}

	tokens, exact := ContextSize(msgs)
	assert.Equal(t, 560, tokens)
	assert.False(t, exact)
}

func TestContextSize_NoUsage(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{
			// 12 chars → ceil(12/4) = 3
			message.TextContent("hello world!"),
		}},
		{Role: message.RoleOutput, Content: []message.Content{
			// 8 chars → ceil(8/4) = 2
			message.TextContent("hi there"),
		}},
	}

	tokens, exact := ContextSize(msgs)
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
			Type:      message.ContentToolInvoke,
			ToolName:  "bash",
			Arguments: map[string]interface{}{"command": "ls -la"},
		},
	}}
	// "bash" = 4 chars, {"command":"ls -la"} = 20 chars → 24 chars → 6
	tokens := EstimateMessage(m)
	assert.Greater(t, tokens, 0)
}

func TestEstimateMessage_ToolResult(t *testing.T) {
	m := message.Message{Content: []message.Content{
		message.ToolResultContent("call-1", "bash", "abcdefgh", false),
	}}
	// call-1 (6) + bash (4) + output (8) = 18 chars → 5 tokens
	assert.Equal(t, 5, EstimateMessage(m))
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

func TestContextFromUsage(t *testing.T) {
	cases := []struct {
		name  string
		usage *message.Usage
		want  int
	}{
		{"nil", nil, 0},
		{"zero", &message.Usage{}, 0},
		{"uncached first turn", &message.Usage{InputTokens: 500, OutputTokens: 50}, 550},
		{
			// The shape figaro actually sees from turn two on: the prompt is
			// almost entirely a cache read, InputTokens is the uncached tail.
			name:  "cache heavy",
			usage: &message.Usage{InputTokens: 2025, OutputTokens: 50, CacheReadTokens: 130_000, CacheWriteTokens: 3_000},
			want:  135_075,
		},
		{"cache write only", &message.Usage{InputTokens: 12, CacheWriteTokens: 20_000, OutputTokens: 8}, 20_020},
		{"cache read only", &message.Usage{CacheReadTokens: 99_000}, 99_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ContextFromUsage(tc.usage))
		})
	}
}

func TestContextSize_CountsCachedInput(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("hi")}, Usage: &message.Usage{
			InputTokens:      2075,
			OutputTokens:     100,
			CacheReadTokens:  120_000,
			CacheWriteTokens: 12_500,
		}},
	}

	got, exact := ContextSize(msgs)
	assert.Equal(t, 134_675, got)
	assert.True(t, exact)

	// A trailing un-metered message estimates only the tail on top of the
	// same watermark base.
	msgs = append(msgs, message.Message{Role: message.RoleInput, Content: []message.Content{
		message.TextContent("now do something else for me please ok?!"), // 40 chars → 10
	}})
	got, exact = ContextSize(msgs)
	assert.Equal(t, 134_685, got)
	assert.False(t, exact)
}
