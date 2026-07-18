package anthropicsdk

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

func sdkBenchLog(b *testing.B, n int) *copyingBenchLog[message.Message] {
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

func appendSDKBenchSuffix(b *testing.B, log store.Log[message.Message]) {
	b.Helper()
	for _, role := range []message.Role{message.RoleUser, message.RoleAssistant} {
		if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("warm " + string(role))},
		}}); err != nil {
			b.Fatal(err)
		}
	}
}

func sdkBenchProvider(cache store.Log[[]json.RawMessage]) *Provider {
	return &Provider{
		reminder: "tag",
		cache:    cache,
	}
}

func BenchmarkCatchUp(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run("Cold/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				log := sdkBenchLog(b, n)
				cache := newCopyingBenchLog[[]json.RawMessage]()
				p := sdkBenchProvider(cache)
				b.StartTimer()
				p.catchUp(log, cache, nil)
			}
		})
		b.Run("WarmDeltaEncode/"+strconv.Itoa(n), func(b *testing.B) {
			prefix := sdkBenchLog(b, n)
			log := sdkBenchLog(b, n)
			appendSDKBenchSuffix(b, log)
			p := sdkBenchProvider(nil)
			p.catchUp(prefix, nil, nil)
			prewarmed := p.projection
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(2, "messages/op")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p.projection = prewarmed
				b.StartTimer()
				p.catchUp(log, nil, nil)
			}
		})
		b.Run("WarmDeltaCached/"+strconv.Itoa(n), func(b *testing.B) {
			prefix := sdkBenchLog(b, n)
			log := sdkBenchLog(b, n)
			appendSDKBenchSuffix(b, log)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := sdkBenchProvider(cache)
			p.catchUp(prefix, cache, nil)
			prewarmed := p.projection
			p.catchUp(log, cache, nil)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(2, "messages/op")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p.projection = prewarmed
				b.StartTimer()
				p.catchUp(log, cache, nil)
			}
		})
		b.Run("WarmSteady/"+strconv.Itoa(n), func(b *testing.B) {
			log := sdkBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := sdkBenchProvider(cache)
			p.catchUp(log, cache, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.catchUp(log, cache, nil)
			}
		})
	}
}

func BenchmarkBuildParams(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			log := sdkBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := sdkBenchProvider(cache)
			projected, err := p.catchUp(log, cache, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildParams(projected.Messages, projected.LogicalTimes, nil, nil, 1024, false, "claude-test")
			}
		})
	}
}

func BenchmarkParseCachedMessages(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			log := sdkBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := sdkBenchProvider(cache)
			if _, err := p.catchUp(log, cache, nil); err != nil {
				b.Fatal(err)
			}
			entries := cache.Read()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var projected projectedMessages
				for _, entry := range entries {
					projected = appendProjectedMessages(projected, entry.Payload, entry.LT)
				}
				if projected.err != nil {
					b.Fatal(projected.err)
				}
			}
		})
	}
}

func BenchmarkMarshalParams(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			log := sdkBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := sdkBenchProvider(cache)
			projected, err := p.catchUp(log, cache, nil)
			if err != nil {
				b.Fatal(err)
			}
			params := buildParams(projected.Messages, projected.LogicalTimes, nil, nil, 1024, false, "claude-test")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := json.Marshal(params); err != nil {
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
			p := sdkBenchProvider(cache)
			for i := 0; i < n; i++ {
				_, _ = cache.Append(store.Entry[[]json.RawMessage]{
					FigaroLT: uint64(i + 1), Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: p.Fingerprint(),
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.invalidateIfStale(cache)
			}
		})
	}
}
