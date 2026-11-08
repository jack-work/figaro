package figaro

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// The per-delta cost of the bus the provider pushes into: what a token pays
// between the SSE decoder and the drain loop.
func BenchmarkTurnBusPushDelta(b *testing.B) {
	bus := newTurnBus(context.Background())
	done := make(chan struct{})
	go func() {
		for range bus.events {
		}
		close(done)
	}()
	c := message.Content{Type: message.ContentProse, Text: "tok"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.PushDelta(c)
	}
	b.StopTimer()
	close(bus.events)
	<-done
}

func BenchmarkTurnBusPushToolDelta(b *testing.B) {
	bus := newTurnBus(context.Background())
	done := make(chan struct{})
	go func() {
		for range bus.events {
		}
		close(done)
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.PushToolInvokeDelta("call", `{"a":`)
	}
	b.StopTimer()
	close(bus.events)
	<-done
}
