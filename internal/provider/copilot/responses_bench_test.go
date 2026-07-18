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

func responsesBenchProvider(cache store.Log[[]json.RawMessage]) *responsesProvider {
	p := newResponsesProvider(
		provider.Knobs{Model: "gpt-test"},
		nil,
		"",
		func(string) (store.Log[[]json.RawMessage], error) { return cache, nil },
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
		b.Run("Warm/"+strconv.Itoa(n), func(b *testing.B) {
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
				_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleUser,
					Content: []message.Content{message.TextContent("warm user")},
				}})
				_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleAssistant,
					Content: []message.Content{message.TextContent("warm assistant")},
				}})
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
