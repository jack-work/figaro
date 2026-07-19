package figaro

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"
)

type benchmarkLog struct {
	rows []store.Entry[message.Message]
}

func (l *benchmarkLog) Read() []store.Entry[message.Message] {
	out := make([]store.Entry[message.Message], len(l.rows))
	copy(out, l.rows)
	return out
}

func (l *benchmarkLog) Len() int { return len(l.rows) }

func (l *benchmarkLog) ReadFrom(figaroLT uint64, n int) []store.Entry[message.Message] {
	start := 0
	if figaroLT > 1 {
		start = int(figaroLT - 1)
		if start > len(l.rows) {
			start = len(l.rows)
		}
	}
	end := len(l.rows)
	if n > 0 && start+n < end {
		end = start + n
	}
	return append([]store.Entry[message.Message](nil), l.rows[start:end]...)
}

func (l *benchmarkLog) ReadPage(from, before uint64, n int) ([]store.Entry[message.Message], int) {
	if before > 0 {
		if n <= 0 {
			return nil, len(l.rows)
		}
		end := int(before - 1)
		if end > len(l.rows) {
			end = len(l.rows)
		}
		start := end - n
		if start < 0 {
			start = 0
		}
		return append([]store.Entry[message.Message](nil), l.rows[start:end]...), len(l.rows)
	}
	return l.ReadFrom(from, n), len(l.rows)
}

func (l *benchmarkLog) Lookup(figaroLT uint64) (store.Entry[message.Message], bool) {
	if figaroLT == 0 || figaroLT > uint64(len(l.rows)) {
		return store.Entry[message.Message]{}, false
	}
	return l.rows[figaroLT-1], true
}

func (l *benchmarkLog) PeekTail() (store.Entry[message.Message], bool) {
	if len(l.rows) == 0 {
		return store.Entry[message.Message]{}, false
	}
	return l.rows[len(l.rows)-1], true
}

func (l *benchmarkLog) Append(e store.Entry[message.Message]) (store.Entry[message.Message], error) {
	e.LT = uint64(len(l.rows) + 1)
	e.FigaroLT = e.LT
	l.rows = append(l.rows, e)
	return e, nil
}

func (l *benchmarkLog) Clear() error {
	l.rows = nil
	return nil
}

func syntheticBenchmarkLog(n int) *benchmarkLog {
	rows := make([]store.Entry[message.Message], n)
	for i := range rows {
		lt := uint64(i + 1)
		role := message.RoleUser
		if i%2 == 1 {
			role = message.RoleAssistant
		}
		rows[i] = store.Entry[message.Message]{
			LT:       lt,
			FigaroLT: lt,
			Payload: message.Message{
				Role:    role,
				Content: []message.Content{message.TextContent("synthetic history")},
			},
		}
	}
	return &benchmarkLog{rows: rows}
}

func BenchmarkComposeLiveTailLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			a := &Agent{
				figLog:      syntheticBenchmarkLog(n),
				turnStartLT: uint64(n - 2),
				gov:         toolout.New(200),
				argPartials: map[string]string{},
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nodes := a.composeTurn(nil)
				runtime.KeepAlive(nodes)
			}
		})
	}
}

func BenchmarkRefreshMetricsLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			cb, _ := chalkboard.Open("")
			cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
				"system.model": json.RawMessage(`"synthetic"`),
			}})
			a := &Agent{figLog: syntheticBenchmarkLog(n), chalkboard: cb}
			b.Cleanup(func() { _ = cb.Close() })
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.refreshMetrics()
			}
		})
	}
}
