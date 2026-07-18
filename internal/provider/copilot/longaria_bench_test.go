package copilot

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func benchmarkWarmResponsesInput(b *testing.B, n int) (*responsesProvider, provider.SendInput) {
	b.Helper()
	figLog := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := newResponsesProvider(
		provider.Knobs{Model: "gpt-5.6-terra", MaxTokens: 1024},
		&staticResponseTokenSource{token: "test-token"},
		"",
		func(string) (store.Log[[]json.RawMessage], error) { return cache, nil },
	)
	fp := p.Fingerprint()
	for i := 0; i < n; i++ {
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		entry, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("synthetic history")},
		}})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := cache.Append(store.Entry[[]json.RawMessage]{
			FigaroLT:    entry.LT,
			Fingerprint: fp,
			Payload: []json.RawMessage{json.RawMessage(fmt.Sprintf(
				`{"type":"message","role":"%s","content":[{"type":"input_text","text":"synthetic history"}]}`,
				role,
			))},
		}); err != nil {
			b.Fatal(err)
		}
	}
	return p, provider.SendInput{AriaID: "benchmark", FigLog: figLog}
}

func BenchmarkWarmResponsesInputLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			p, in := benchmarkWarmResponsesInput(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				input, err := p.inputFor(in)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(input)
			}
		})
	}
}
