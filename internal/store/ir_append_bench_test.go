package store

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// BenchmarkIRAppend is one fig IR append on a real backend, which is the
// operation the stamp hoist changed: the stamp used to be recovered by
// OPENING THE NODE AGAIN and reading the record back out (openOnce ->
// openNode -> ReadAt -> Close), and now comes back from the append that
// wrote it.
func BenchmarkIRAppend(b *testing.B) {
	be, aria := NewTestAria(b, "d", message.Patch{})
	ir, err := be.OpenFigIR(aria)
	if err != nil {
		b.Fatal(err)
	}
	msg := message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("a turn body of a plausible size for one message")},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: msg}); err != nil {
			b.Fatal(err)
		}
	}
}
