package figaro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// The window between the durable append and OpenInquiry. realisticLog stamps
// usage on every assistant message so tokens.ContextSize can take its shortcut.
func realisticLog(tb testing.TB, n int, withUsage bool) store.Log[message.Message] {
	tb.Helper()
	log := store.NewMemLog[message.Message]()
	prose := strings.Repeat("some prose from the user. ", 20) // ~520B
	output := strings.Repeat("tool output line\n", 250)       // ~4.2KB
	for i := 0; i < n; i++ {
		var m message.Message
		if i%2 == 0 {
			m = message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(prose)}}
		} else {
			m = message.Message{
				Role:    message.RoleOutput,
				Content: []message.Content{message.TextContent(output)},
			}
			if withUsage {
				m.Usage = &message.Usage{
					InputTokens: 120, OutputTokens: 300,
					CacheReadTokens: 90_000, CacheWriteTokens: 2_000,
				}
			}
		}
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			tb.Fatal(err)
		}
	}
	return log
}

func gapAgent(tb testing.TB, n int, withUsage bool) *Agent {
	tb.Helper()
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model": json.RawMessage(`"claude-sonnet-4-5"`),
		"mantra":       json.RawMessage(`"benchmark"`),
	}, nil))
	tb.Cleanup(func() { _ = cb.Close() })
	a := &Agent{
		figLog:  realisticLog(tb, n, withUsage),
		form:    cb,
		proj:    uiir.New(nil),
		ariaSrv: aria.NewServer(),
	}
	a.bindProvider(perfProvider{})
	a.refreshMetricsFrom(a.Context())
	return a
}

// Two regimes of refreshMetrics after a prompt lands: incremental (one row
// behind the tail) and the full fallback (tail.LT < metricsLT).
func BenchmarkPromptBroadcastGap(b *testing.B) {
	for _, n := range []int{100, 1_000, 5_000} {
		for _, usage := range []bool{true, false} {
			label := "usage"
			if !usage {
				label = "nousage"
			}
			b.Run(fmt.Sprintf("messages=%d/%s/incremental", n, label), func(b *testing.B) {
				a := gapAgent(b, n, usage)
				tail, _ := a.figLog.PeekTail()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					// One row behind the tail, where an append leaves it.
					a.mu.Lock()
					a.metricsLT = tail.LT - 1
					a.mu.Unlock()
					a.refreshMetrics()
				}
			})
			b.Run(fmt.Sprintf("messages=%d/%s/fallback", n, label), func(b *testing.B) {
				a := gapAgent(b, n, usage)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					a.refreshMetricsFrom(a.Context())
				}
			})
		}
	}
}

// The two non-metrics costs in the same window.
func BenchmarkPromptBroadcastControl(b *testing.B) {
	for _, n := range []int{100, 1_000, 5_000} {
		b.Run(fmt.Sprintf("messages=%d/open-inquiry", n), func(b *testing.B) {
			a := gapAgent(b, n, true)
			a.turnID = 7
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.ariaSrv.OpenInquiry(a.turnID, "what is the gap?", nil)
			}
		})
		b.Run(fmt.Sprintf("messages=%d/lock-control", n), func(b *testing.B) {
			a := gapAgent(b, n, true)
			tail, _ := a.figLog.PeekTail()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.mu.Lock()
				a.metricsLT = tail.LT - 1
				a.mu.Unlock()
			}
		})
	}
}
