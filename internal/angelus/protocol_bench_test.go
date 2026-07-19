package angelus

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type readBenchLog struct {
	*store.MemLog[message.Message]
}

func (l *readBenchLog) Read() []store.Entry[message.Message] {
	rows := l.MemLog.Read()
	out := make([]store.Entry[message.Message], len(rows))
	copy(out, rows)
	return out
}

type readBenchBackend struct {
	store.Backend
	log store.Log[message.Message]
}

func (b readBenchBackend) Open(string) (store.Log[message.Message], error) {
	return b.log, nil
}

func (b readBenchBackend) Node(id string) (store.NodeView, bool) {
	return store.NodeView{ID: id, Kind: conversationKind}, id == "perf"
}

func newReadBench(b *testing.B, req rpc.AriaReadRequest) (*handlers, []byte) {
	b.Helper()
	log := &readBenchLog{MemLog: store.NewMemLog[message.Message]()}
	for i := 0; i < 10_000; i++ {
		if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{message.TextContent(fmt.Sprintf("message %d", i))},
		}}); err != nil {
			b.Fatal(err)
		}
	}
	backend := readBenchBackend{log: log}
	h := &handlers{angelus: &Angelus{Backend: backend}}
	req.FigaroID = "perf"
	params, _ := json.Marshal(req)
	return h, params
}

func BenchmarkAriaReadPage10000(b *testing.B) {
	h, params := newReadBench(b, rpc.AriaReadRequest{From: 9_900, Limit: 100})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.ariaRead(b.Context(), params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAriaReadBefore10000(b *testing.B) {
	h, params := newReadBench(b, rpc.AriaReadRequest{Before: 9_900, Limit: 100})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.ariaRead(b.Context(), params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDormantList(b *testing.B) {
	for _, n := range []int{0, 100, 300} {
		b.Run(fmt.Sprintf("arias-%d", n), func(b *testing.B) {
			oldLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
			b.Cleanup(func() { slog.SetDefault(oldLog) })

			backend, err := store.NewXwalBackend(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = backend.Close() })
			if n > 0 {
				loadout, err := backend.CreateLoadout("perf", message.Patch{Set: map[string]json.RawMessage{
					"system.provider": json.RawMessage(`"perf"`),
					"system.model":    json.RawMessage(`"perf-model"`),
				}})
				if err != nil {
					b.Fatal(err)
				}
				for i := 0; i < n; i++ {
					id, err := backend.CreateConversation(loadout)
					if err != nil {
						b.Fatal(err)
					}
					if err := backend.ApplyChalkboard(id, message.Patch{Set: map[string]json.RawMessage{
						"mantra":     json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("aria %d", i))),
						"system.cwd": json.RawMessage(`"/work"`),
					}}); err != nil {
						b.Fatal(err)
					}
					log, err := backend.Open(id)
					if err != nil {
						b.Fatal(err)
					}
					for j := 0; j < 2; j++ {
						if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
							Role:    message.RoleUser,
							Content: []message.Content{message.TextContent("message")},
						}}); err != nil {
							b.Fatal(err)
						}
					}
					if err := backend.SetMeta(id, &store.AriaMeta{
						MessageCount: 2,
						LastFigaroLT: 4,
						Provider:     "perf",
						Model:        "perf-model",
						Mantra:       fmt.Sprintf("aria %d", i),
						Cwd:          "/work",
					}); err != nil {
						b.Fatal(err)
					}
				}
			}
			h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}
			if _, err := h.list(b.Context(), nil); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ReportMetric(float64(n), "arias")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := h.list(b.Context(), nil)
				if err != nil {
					b.Fatal(err)
				}
				if len(got.(rpc.ListResponse).Figaros) != n {
					b.Fatalf("list returned %d arias, want %d", len(got.(rpc.ListResponse).Figaros), n)
				}
			}
		})
	}
}
