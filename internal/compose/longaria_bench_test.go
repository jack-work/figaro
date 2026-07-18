package compose

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func BenchmarkUnitsLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			msgs := make([]message.Message, n)
			for i := range msgs {
				role := message.RoleUser
				if i%2 == 1 {
					role = message.RoleAssistant
				}
				msgs[i] = message.Message{
					LogicalTime: uint64(i + 1),
					Role:        role,
					Content:     []message.Content{message.TextContent("synthetic history")},
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				units := Units(msgs, nil, nil)
				runtime.KeepAlive(units)
			}
		})
	}
}
