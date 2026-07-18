package figaro

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

type perfProvider struct{}

func (perfProvider) Name() string                                         { return "perf" }
func (perfProvider) Fingerprint() string                                  { return "perf-v1" }
func (perfProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (perfProvider) SetModel(string)                                      {}
func (perfProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	return nil
}

func longMemLog(tb testing.TB, n int) store.Log[message.Message] {
	tb.Helper()
	log := store.NewMemLog[message.Message]()
	text := strings.Repeat("x", 256)
	for i := 0; i < n; i++ {
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent(text)},
		}})
		if err != nil {
			tb.Fatal(err)
		}
	}
	return log
}

func BenchmarkAgentRestoreHistory10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := chalkboard.Open("")
	a := &Agent{figLog: log, prov: perfProvider{}, chalkboard: cb}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		appendInterruptSentinelIfDangling(a.figLog, "perf")
		messages := unwrapMessages(a.figLog.Read())
		a.refreshMetricsFrom(messages)
		units := compose.Units(messages, nil, nil)
		if len(units) == 0 {
			b.Fatal("no restored units")
		}
	}
}

func BenchmarkInterruptRepair10000(b *testing.B) {
	log := longMemLog(b, 10_000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		appendInterruptSentinelIfDangling(log, "perf")
	}
}

func BenchmarkAgentInfo10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := chalkboard.Open("")
	a := &Agent{figLog: log, prov: perfProvider{}, chalkboard: cb, inbox: NewInbox(context.Background())}
	a.refreshMetrics()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Info()
	}
}

func BenchmarkAgentMetricRefresh10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := chalkboard.Open("")
	a := &Agent{figLog: log, prov: perfProvider{}, chalkboard: cb}
	a.refreshMetrics()

	b.Run("full-fold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			msgs := a.Context()
			sumUsage(msgs)
			tokens.ContextSize(msgs)
			message.CountMessages(msgs)
		}
	})
	b.Run("incremental-hot", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.refreshMetrics()
		}
	})
}

func BenchmarkDerivedPublish10000(b *testing.B) {
	_ = longMemLog(b, 10_000)
	evt := DerivationEvent{
		Metadata: store.AriaMeta{
			MessageCount:     10_000,
			TurnCount:        5_000,
			TokensIn:         100_000,
			TokensOut:        50_000,
			CacheReadTokens:  25_000,
			CacheWriteTokens: 5_000,
			Provider:         "perf",
			Model:            "perf-model",
			ContextTokens:    640_000,
			ContextExact:     true,
			LastFigaroLT:     10_000,
		},
		LastUpdateMS: 1,
	}
	derivations := []DurableDerivation{
		&summaryDerivation{},
		&usageDerivation{ariaID: "perf", providerName: "perf"},
		&metaDerivation{ariaID: "perf", providerName: "perf"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, derivation := range derivations {
			if err := derivation.OnTick(io.Discard, evt); err != nil {
				b.Fatal(err)
			}
		}
	}
}
