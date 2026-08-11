package angelus

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// benchStore writes n message pairs into a real xwal-backed aria and
// returns the backend and id. Sized to bracket the real range: 600 is a
// working conversation, 10k is a long one, 50k is the pathological case in
// the eviction comment.
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
	log, err := backend.Open(id)
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

// BenchmarkAriaReaderPage is the cost of serving ONE page with no agent
// resident. It is the number that decides whether hibernation pays for
// itself: reclaiming an agent is only a win if reading it back is cheap.
//
// It is currently O(whole history) per page, not O(page): every call
// decodes the log and projects every turn just to serve one window. That is
// the known cost of the first pass and the thing a range-projecting reader
// would fix. Measure it before optimising it.
func BenchmarkAriaReaderPage(b *testing.B) {
	for _, n := range []int{600, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			backend, id := benchStore(b, n)
			r := NewAriaReader(backend, uiir.New(nil))

			// Warm the memoized log so the measurement is projection, not
			// first-open disk I/O, a second reader hits the same instance.
			if _, err := r.Page(id, aria.Anchor{}, 65536, false); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := r.Page(id, aria.Anchor{}, 65536, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAriaReaderTail pages backward from the end, which is what a
// terminal attaching to an aria actually does. Same O(history) projection,
// so the gap between this and Page is only the window walk.
func BenchmarkAriaReaderTail(b *testing.B) {
	for _, n := range []int{600, 10_000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			backend, id := benchStore(b, n)
			r := NewAriaReader(backend, uiir.New(nil))
			if _, err := r.Page(id, aria.Anchor{}, 65536, true); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := r.Page(id, aria.Anchor{}, 65536, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAriaReaderForm is the cheap read, kept honest: a board is
// a reducible channel replay and must not become the reason a page is slow.
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

// BenchmarkAriaReaderContext measures the fig-IR read without projection,
// which isolates how much of a page is decode and how much is compose.
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
