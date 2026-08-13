package store

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

func BenchmarkCachedLogReadLongAria(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			inner := NewMemLog[message.Message]()
			for i := 0; i < n; i++ {
				_, _ = inner.Append(Entry[message.Message]{Payload: message.Message{
					Role:    message.RoleOutput,
					Content: []message.Content{message.TextContent("synthetic history")},
				}})
			}
			cached := newCachedLog[message.Message](inner)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows := cached.Read()
				runtime.KeepAlive(rows)
			}
		})
	}
}

// slowAppendLog stands in for a synced append: the cost that used to sit
// inside cachedLog's read lock, and the reason readers waited on it.
type slowAppendLog struct {
	Log[message.Message]
	delay time.Duration
}

func (l *slowAppendLog) Append(e Entry[message.Message]) (Entry[message.Message], error) {
	time.Sleep(l.delay)
	return l.Log.Append(e)
}

// What a reader pays while an appender is mid-sync. This is the BEFORE for
// turning cachedLog's RWMutex into a published snapshot: with the lock
// covering rows, trimmed and byFK, a reader waits for the writer's cache
// update; it must never wait for the append itself, which is what the
// separate appendMu already guarantees.
//
// If a change makes readers wait on the append again, this benchmark is
// where it shows: the reader's ns/op goes from tens to milliseconds.
func BenchmarkCachedLogReadWhileAppending(b *testing.B) {
	inner := NewMemLog[message.Message]()
	for i := 0; i < 5_000; i++ {
		_, _ = inner.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, Content: []message.Content{message.TextContent("history")},
		}})
	}
	c := newCachedLog[message.Message](&slowAppendLog{Log: inner, delay: 3 * time.Millisecond})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = c.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}})
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.PeekTail(); !ok {
			b.Fatal("no tail")
		}
	}
	b.StopTimer()
	close(stop)
	<-done
}
