package store

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func BenchmarkCachedLogReadLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			inner := NewMemLog[message.Message]()
			for i := 0; i < n; i++ {
				_, _ = inner.Append(Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleAssistant,
					Content: []message.Content{message.TextContent("synthetic history")},
				}})
			}
			cached := newCachedLog[message.Message](inner)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows := cached.Read()
				runtime.KeepAlive(rows)
			}
		})
	}
}
