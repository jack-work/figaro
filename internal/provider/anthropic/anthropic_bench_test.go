package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

// Benchmark the projection hot path. projectMessagesWithModel is
// called once per Send.

func benchMessages(nMessages int) []message.Message {
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
	return msgs
}

func benchTools() []provider.Tool {
	return []provider.Tool{
		{Name: "bash", Description: "shell", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "edit", Description: "edit", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "read", Description: "read", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "write", Description: "write", Parameters: map[string]interface{}{"type": "object"}},
	}
}

func BenchmarkProjectMessages_10msgs(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tag"}
	pre := a.encodeAll(benchMessages(10))
	tools := benchTools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = a.projectMessagesWithModel(pre, nil, tools, 1024, false, "claude-test")
	}
}

func BenchmarkProjectMessages_100msgs(b *testing.B) {
	a := &Anthropic{ReminderRenderer: "tag"}
	pre := a.encodeAll(benchMessages(100))
	tools := benchTools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = a.projectMessagesWithModel(pre, nil, tools, 1024, false, "claude-test")
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
