package anthropic

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

type copyingBenchLog[T any] struct {
	*store.MemLog[T]
}

func newCopyingBenchLog[T any]() *copyingBenchLog[T] {
	return &copyingBenchLog[T]{MemLog: store.NewMemLog[T]()}
}

func (l *copyingBenchLog[T]) Read() []store.Entry[T] {
	entries := l.MemLog.Read()
	out := make([]store.Entry[T], len(entries))
	copy(out, entries)
	return out
}

func directBenchLog(b *testing.B, n int) *copyingBenchLog[message.Message] {
	b.Helper()
	log := newCopyingBenchLog[message.Message]()
	for i := 0; i < n; i++ {
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("turn body " + strconv.Itoa(i))},
		}})
		if err != nil {
			b.Fatal(err)
		}
	}
	return log
}

func BenchmarkCatchUp(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run("Cold/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				log := directBenchLog(b, n)
				cache := newCopyingBenchLog[[]json.RawMessage]()
				a := &Anthropic{ReminderRenderer: "tag"}
				b.StartTimer()
				a.catchUp("bench", log, cache, nil)
			}
		})
		b.Run("Warm/"+strconv.Itoa(n), func(b *testing.B) {
			log := directBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			a := &Anthropic{ReminderRenderer: "tag"}
			a.catchUp("bench", log, cache, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleUser,
					Content: []message.Content{message.TextContent("warm user")},
				}})
				_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleAssistant,
					Content: []message.Content{message.TextContent("warm assistant")},
				}})
				a.catchUp("bench", log, cache, nil)
			}
		})
	}
}

func BenchmarkProjectMessagesLong(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			a := &Anthropic{ReminderRenderer: "tag"}
			perMessage := a.encodeAll(benchMessages(n))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := a.projectMessagesWithModel(perMessage, nil, benchTools(), 1024, false, "claude-test"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkInvalidateIfStale(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			cache := newCopyingBenchLog[[]json.RawMessage]()
			a := &Anthropic{ReminderRenderer: "tag"}
			for i := 0; i < n; i++ {
				_, _ = cache.Append(store.Entry[[]json.RawMessage]{
					FigaroLT: uint64(i + 1), Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: a.Fingerprint(),
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.invalidateIfStale(cache)
			}
		})
	}
}
