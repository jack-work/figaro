package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

// Benchmark the projection hot path. projectBlockWithModel is called
// once per Send; markCacheBreakpoints is called inside it; applyRenderer
// is called immediately after on the Send path. We measure each
// component separately and the combined cost.

func benchBlock(nMessages int) *message.Block {
	msgs := make([]message.Message, nMessages)
	for i := range msgs {
		// Leaf must be user-role for the realistic projection.
		role := message.RoleAssistant
		if i%2 == 0 {
			role = message.RoleUser
		}
		// If nMessages is even, the leaf would be assistant. Force user.
		if i == nMessages-1 {
			role = message.RoleUser
		}
		msgs[i] = message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("turn body number " + itoa(i))},
		}
	}
	block := message.NewBlockOfMessages(msgs)
	block.Header = &message.Message{
		Role:    message.RoleSystem,
		Content: []message.Content{message.TextContent("you are figaro")},
	}
	return block
}

func benchTools() []provider.Tool {
	return []provider.Tool{
		{Name: "bash", Description: "shell", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "edit", Description: "edit", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "read", Description: "read", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "write", Description: "write", Parameters: map[string]interface{}{"type": "object"}},
	}
}

func BenchmarkProjectBlock_10msgs(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tag"}
	block := benchBlock(10)
	tools := benchTools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.projectBlockWithModel(block, tools, 1024, false, "claude-test")
	}
}

func BenchmarkProjectBlock_100msgs(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tag"}
	block := benchBlock(100)
	tools := benchTools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.projectBlockWithModel(block, tools, 1024, false, "claude-test")
	}
}

func BenchmarkApplyRenderer_Tag_5reminders(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tag"}
	reminders := []chalkboard.RenderedEntry{
		{Key: "cwd", Body: "Working directory: /home/figaro"},
		{Key: "datetime", Body: "Current time: 10AM EDT"},
		{Key: "model", Body: "Model: claude-opus-4-6"},
		{Key: "root", Body: "Project root: /home/figaro"},
		{Key: "label", Body: "Aria label: morning"},
	}
	makeReq := func() *nativeRequest {
		req := a.projectBlockWithModel(benchBlock(10), benchTools(), 1024, false, "claude-test")
		return &req
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := makeReq()
		a.applyRenderer(req, reminders)
	}
}

func BenchmarkApplyRenderer_Tool_5reminders(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tool"}
	reminders := []chalkboard.RenderedEntry{
		{Key: "cwd", Body: "Working directory: /home/figaro"},
		{Key: "datetime", Body: "Current time: 10AM EDT"},
		{Key: "model", Body: "Model: claude-opus-4-6"},
		{Key: "root", Body: "Project root: /home/figaro"},
		{Key: "label", Body: "Aria label: morning"},
	}
	makeReq := func() *nativeRequest {
		req := a.projectBlockWithModel(benchBlock(10), benchTools(), 1024, false, "claude-test")
		return &req
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := makeReq()
		a.applyRenderer(req, reminders)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	j := len(buf)
	for i > 0 {
		j--
		buf[j] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[j:])
}
