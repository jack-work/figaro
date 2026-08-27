package openaichat

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
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

func benchLog(b *testing.B, n int) *copyingBenchLog[message.Message] {
	b.Helper()
	log := newCopyingBenchLog[message.Message]()
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent("turn body " + strconv.Itoa(i))},
		}}); err != nil {
			b.Fatal(err)
		}
	}
	return log
}

func benchProvider(b *testing.B, mode provider.MarkMode) *Provider {
	b.Helper()
	p, err := New(provider.Knobs{Model: "claude-sonnet-4-6", MaxTokens: 1024},
		staticToken("k"), provider.GatewayRoute("http://127.0.0.1:61890/v1"), nil)
	if err != nil {
		b.Fatal(err)
	}
	p.setMarkMode(mode)
	return p
}

// BenchmarkCatchUp measures ONE SEND'S WHOLE TRANSLATION PATH on this
// dialect: the catch-up AND the assembly read that follows it.
//
// IT IS NOT AN O(new) GUARD, and it stopped being one when the memo went.
// catchUp ends in provider.Rows, which reads the whole row log: assembly is
// O(history) per send BY DESIGN (the provider asks the log for the full
// history and marshals it). The O(new) property now belongs to
// provider.CatchUp alone and is asserted by count in
// TestCatchUpVisitsOnlyWhatIsNew; what O(history) costs is measured by
// BenchmarkTranslations.
func BenchmarkCatchUp(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run("Cold/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				log := benchLog(b, n)
				cache := newCopyingBenchLog[[]json.RawMessage]()
				p := benchProvider(b, provider.MarkAuto)
				b.StartTimer()
				drainCatchUp(p.catchUp(log, cache, nil, nil))
			}
		})
		b.Run("WarmDelta/"+strconv.Itoa(n), func(b *testing.B) {
			prefix := benchLog(b, n)
			log := benchLog(b, n)
			for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
				if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:    role,
					Content: []message.Content{message.TextContent("warm " + string(role))},
				}}); err != nil {
					b.Fatal(err)
				}
			}
			cache := newCopyingBenchLog[[]json.RawMessage]()
			p := benchProvider(b, provider.MarkAuto)
			drainCatchUp(p.catchUp(prefix, cache, nil, nil))
			drainCatchUp(p.catchUp(log, cache, nil, nil))
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(2, "messages/op")
			for i := 0; i < b.N; i++ {
				drainCatchUp(p.catchUp(log, cache, nil, nil))
			}
		})
	}
}

// BenchmarkAssemble measures the per-turn request build, which IS
// proportional to prompt bytes (serialization always is): the invariant it
// guards is that marking does not add a second full pass.
func BenchmarkAssemble(b *testing.B) {
	board := form.FromMap(map[string]json.RawMessage{
		"system.credo": json.RawMessage(`"you are figaro"`),
	})
	for _, n := range []int{1_000, 10_000} {
		for _, mode := range []provider.MarkMode{provider.MarkAuto, provider.MarkBlocks} {
			b.Run(string(mode)+"/"+strconv.Itoa(n), func(b *testing.B) {
				p := benchProvider(b, mode)
				log := benchLog(b, n)
				perMessage, err := p.catchUp(log, newCopyingBenchLog[[]json.RawMessage](), nil, nil)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := p.assemble(perMessage, board, nil, 1024, "aria1"); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkMarkRequest isolates the marking pass itself. It walks messages
// to find the system head and the tail, so it must stay flat in prompt size
// rather than scanning every message's parts.
func BenchmarkMarkRequest(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			p := benchProvider(b, provider.MarkBlocks)
			log := benchLog(b, n)
			perMessage, err := p.catchUp(log, newCopyingBenchLog[[]json.RawMessage](), nil, nil)
			if err != nil {
				b.Fatal(err)
			}
			board := form.FromMap(map[string]json.RawMessage{
				"system.credo": json.RawMessage(`"you are figaro"`),
			})
			req, err := p.assemble(perMessage, board, nil, 1024, "aria1")
			if err != nil {
				b.Fatal(err)
			}
			policy := provider.ResolveCachePolicy(board)
			plan := p.plan()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				markRequest(&req, policy, plan, p.Route.Caps)
			}
		})
	}
}

// drainCatchUp walks the sequence a catch-up hands back: the sequence is lazy,
// so a benchmark that only calls catchUp measures the translation and not the
// read.
func drainCatchUp(seq provider.RowSeq, err error) int {
	if err != nil || seq == nil {
		return 0
	}
	n := 0
	for range seq {
		n++
	}
	return n
}
