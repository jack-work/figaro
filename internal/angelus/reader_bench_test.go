package angelus

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func benchStore(tb testing.TB, n int) (store.Backend, string) {
	tb.Helper()
	backend, err := store.NewXwalBackend(tb.TempDir(), 0)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { backend.Close() })

	outfit, err := backend.CreateOutfit("bench", message.Patch{})
	if err != nil {
		tb.Fatal(err)
	}
	id, err := backend.CreateConversation(outfit)
	if err != nil {
		tb.Fatal(err)
	}
	log, err := backend.OpenFigIR(id)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		turn := uint64(i/2 + 1)
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		m := message.Message{
			Role:    role,
			TurnID:  turn,
			Content: []message.Content{{Type: message.ContentProse, Text: benchText(i)}},
		}
		if role == message.RoleOutput {
			m.Usage = &message.Usage{InputTokens: 10, OutputTokens: 20}
		}
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			tb.Fatal(err)
		}
	}
	return backend, id
}

func benchText(i int) string {
	return fmt.Sprintf("message %d: %s", i,
		"the quick brown fox jumps over the lazy dog, and does so at some length ")
}

func BenchmarkAriaReaderPage(b *testing.B) {
	for _, n := range []int{600, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			backend, id := benchStore(b, n)
			r := NewAriaReader(backend, uiir.New(nil))

			// Warm the log so the measurement is projection, not first-open I/O.
			if _, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, false); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAriaReaderTail(b *testing.B) {
	for _, n := range []int{600, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			backend, id := benchStore(b, n)
			r := NewAriaReader(backend, uiir.New(nil))
			if _, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, true); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAriaReaderForm(b *testing.B) {
	backend, id := benchStore(b, 600)
	r := NewAriaReader(backend, uiir.New(nil))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := r.Form(id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAriaReaderContext(b *testing.B) {
	for _, n := range []int{600, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			backend, id := benchStore(b, n)
			r := NewAriaReader(backend, uiir.New(nil))
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := r.Context(id); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
