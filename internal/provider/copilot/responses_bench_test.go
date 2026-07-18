package copilot

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
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

func responsesBenchLog(b *testing.B, n int) *copyingBenchLog[message.Message] {
	b.Helper()
	log := newCopyingBenchLog[message.Message]()
	for i := 0; i < n; i++ {
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		var usage *message.Usage
		if role == message.RoleAssistant {
			usage = &message.Usage{InputTokens: i + 100, OutputTokens: 20}
		}
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("turn body " + strconv.Itoa(i))},
			Usage:   usage,
		}})
		if err != nil {
			b.Fatal(err)
		}
	}
	return log
}

func appendResponsesBenchSuffix(b *testing.B, log store.Log[message.Message]) {
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

func responsesBenchProvider(cache store.Log[[]json.RawMessage]) *responsesProvider {
	var cacheOpen func(string) (store.Log[[]json.RawMessage], error)
	if cache != nil {
		cacheOpen = func(string) (store.Log[[]json.RawMessage], error) { return cache, nil }
	}
	p := newResponsesProvider(
		provider.Knobs{Model: "gpt-test"},
		nil,
		"",
		cacheOpen,
	)
	return p
}

func BenchmarkInputFor(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run("Cold/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				log := responsesBenchLog(b, n)
				cache := newCopyingBenchLog[[]json.RawMessage]()
				p := responsesBenchProvider(cache)
				in := provider.SendInput{AriaID: "bench", FigLog: log}
				b.StartTimer()
				if _, err := p.inputFor(in); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("WarmDeltaEncode/"+strconv.Itoa(n), func(b *testing.B) {
			prefix := responsesBenchLog(b, n)
			log := responsesBenchLog(b, n)
			appendResponsesBenchSuffix(b, log)
			p := responsesBenchProvider(nil)
			if _, err := p.inputFor(provider.SendInput{AriaID: "bench", FigLog: prefix}); err != nil {
				b.Fatal(err)
			}
			prewarmed := p.projection
			in := provider.SendInput{AriaID: "bench", FigLog: log}
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(2, "messages/op")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p.projection = prewarmed
				b.StartTimer()
				if _, err := p.inputFor(in); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("WarmDeltaCached/"+strconv.Itoa(n), func(b *testing.B) {
			prefix := responsesBenchLog(b, n)
			log := responsesBenchLog(b, n)
			appendResponsesBenchSuffix(b, log)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := responsesBenchProvider(cache)
			if _, err := p.inputFor(provider.SendInput{AriaID: "bench", FigLog: prefix}); err != nil {
				b.Fatal(err)
			}
			prewarmed := p.projection
			in := provider.SendInput{AriaID: "bench", FigLog: log}
			if _, err := p.inputFor(in); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(2, "messages/op")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p.projection = prewarmed
				b.StartTimer()
				if _, err := p.inputFor(in); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("WarmSteady/"+strconv.Itoa(n), func(b *testing.B) {
			log := responsesBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := responsesBenchProvider(cache)
			in := provider.SendInput{AriaID: "bench", FigLog: log}
			if _, err := p.inputFor(in); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := p.inputFor(in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateContext(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			log := responsesBenchLog(b, n)
			p := responsesBenchProvider(newCopyingBenchLog[[]json.RawMessage]())
			p.SetContextLimits("gpt-test", responseContextLimits{Default: 1_000_000})
			in := provider.SendInput{AriaID: "bench", FigLog: log}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := p.validateContext(in, "gpt-test", responseRequestOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarshalResponseRequest(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			log := responsesBenchLog(b, n)
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := responsesBenchProvider(cache)
			input, err := p.inputFor(provider.SendInput{AriaID: "bench", FigLog: log})
			if err != nil {
				b.Fatal(err)
			}
			request := responseCreateRequest{Type: "response.create", Input: input, Model: "gpt-test"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := json.Marshal(request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCacheValidation(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			cache := newCopyingBenchLog[[]json.RawMessage]()
			for i := 0; i < n; i++ {
				_, _ = cache.Append(store.Entry[[]json.RawMessage]{
					FigaroLT: uint64(i + 1), Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: responseFingerprint("gpt-test"),
				})
			}
			p := responsesBenchProvider(cache)
			fingerprint := responseFingerprint("gpt-test")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.invalidateCache(cache, fingerprint)
			}
		})
	}
}
