package figaro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livelog/aria"
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
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
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
	cb, _ := form.Open("")
	a := &Agent{figLog: log, form: cb}
	a.bindProvider(perfProvider{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repairInterruptedTail(a.figLog, "perf")
		messages := unwrapMessages(a.figLog.Read())
		a.refreshMetricsFrom(messages)
		turns := compose.Turns(messages)
		if len(turns) == 0 {
			b.Fatal("no restored turns")
		}
	}
}

// BenchmarkInterruptRepairScanOnly10000 prices the scan that finds nothing:
// prose-only rows return at repairInterruptedTail's first guard.
func BenchmarkInterruptRepairScanOnly10000(b *testing.B) {
	log := longMemLog(b, 10_000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repairInterruptedTail(log, "perf")
	}
}

// danglingToolLog ends in an unanswered tool_invoke: the only input that drives
// repairInterruptedTail past its first guard.
func danglingToolLog(tb testing.TB, n int) store.Log[message.Message] {
	tb.Helper()
	log := longMemLog(tb, n)
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput,
		Content: []message.Content{
			message.TextContent("about to run something"),
			{Type: message.ContentToolInvoke, ToolCallID: "call_dangling", ToolName: "bash",
				Arguments: map[string]any{"command": "sleep 90"}},
		},
	}})
	if err != nil {
		tb.Fatal(err)
	}
	return log
}

// The fixture is asserted before it is timed, or it drifts back to measuring the scan.
func BenchmarkInterruptRepairDangling10000(b *testing.B) {
	log := danglingToolLog(b, 10_000)
	if _, repaired := repairInterruptedTail(log, "guard"); !repaired {
		b.Fatal("the fixture was not repaired, so this benchmark measures the scan and not the repair; " +
			"danglingToolLog must leave an unanswered tool_invoke at the tail")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Each iteration rebuilds: repairInterruptedTail appends, so a shared log is repaired once.
		b.StopTimer()
		fresh := danglingToolLog(b, 10_000)
		b.StartTimer()
		repairInterruptedTail(fresh, "perf")
	}
}

func BenchmarkAgentInfo10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := form.Open("")
	a := &Agent{figLog: log, form: cb, inbox: NewInbox(context.Background())}
	a.bindProvider(perfProvider{})
	a.refreshMetrics()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Info()
	}
}

func BenchmarkAgentMetricRefresh10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := form.Open("")
	a := &Agent{figLog: log, form: cb}
	a.bindProvider(perfProvider{})
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

func BenchmarkMetadataPublication10000(b *testing.B) {
	log := longMemLog(b, 10_000)
	cb, _ := form.Open("")
	backend := &metadataCaptureBackend{}
	a := &Agent{
		id:      "perf",
		figLog:  log,
		form:    cb,
		backend: backend,
	}
	a.bindProvider(perfProvider{})
	a.refreshMetrics()

	b.Run("legacy-two-history-reads", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			meta := &store.AriaMeta{}
			for _, e := range a.figLog.Read() {
				if e.Payload.Role == message.RoleOutput {
					meta.TurnCount++
				}
				meta.LastFigaroLT = e.LT
			}
			meta.MessageCount = message.CountMessages(unwrapMessages(a.figLog.Read()))
			if meta.MessageCount != 10_000 {
				b.Fatalf("message count = %d", meta.MessageCount)
			}
		}
	})
	b.Run("actor-snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.publishMetadata()
		}
	})
}

func BenchmarkLiveFramePersistence(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "_live")
	path := filepath.Join(dir, "perf.json")
	markdown := strings.Repeat("prose ", 700)
	output := strings.Repeat("tool output\n", 700)

	run := func(b *testing.B, persist bool) {
		srv := aria.NewServer()
		srv.OpenTurn(uint64(1))
		a := &Agent{ariaSrv: srv}
		nodes := []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: markdown + "a"},
			{
				Type:   livedoc.NodeTool,
				ID:     "tool-1",
				Name:   "shell",
				Args:   map[string]interface{}{"command": "go test ./..."},
				Status: livedoc.StatusRunning,
				Output: output,
			},
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if i&1 == 0 {
				nodes[0].Markdown = markdown + "a"
			} else {
				nodes[0].Markdown = markdown + "b"
			}
			a.emitDelta(nil, nodes, 0)
			if persist {
				blob, err := json.Marshal(aria.Message{
					Turn:  1,
					Role:  string(message.RoleOutput),
					Nodes: nodes,
				})
				if err != nil {
					b.Fatal(err)
				}
				if err := os.MkdirAll(dir, 0o700); err != nil {
					b.Fatal(err)
				}
				tmp := path + ".tmp"
				if err := os.WriteFile(tmp, blob, 0o644); err != nil {
					b.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					b.Fatal(err)
				}
			}
		}
	}

	b.Run("legacy-unread-blob", func(b *testing.B) { run(b, true) })
	b.Run("in-memory-only", func(b *testing.B) { run(b, false) })
}
