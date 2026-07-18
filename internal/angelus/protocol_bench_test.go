package angelus

import (
	"encoding/json"
	"fmt"
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
