package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// The door benchmarks measure what every client pays: one call on an open
// connection, a new client's catch-up, a fresh process's first touch, and
// what a following connection costs the daemon.

func quietBench(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
}

func benchAria(b *testing.B, turns int) (*sdk.Angelus, string, func(onNotify sdk.NotifyHandler) *sdk.Aria, context.Context) {
	b.Helper()
	quietBench(b)
	a, acli, ctx := daemonFixture(b)
	created, err := acli.Create(ctx, dress(b, "mock"), nil)
	if err != nil {
		b.Fatal(err)
	}
	dial := func(onNotify sdk.NotifyHandler) *sdk.Aria {
		c, err := sdk.DialAria(ariaAddr(a, created.FigaroID), onNotify)
		if err != nil {
			b.Fatal(err)
		}
		return c
	}
	if turns > 0 {
		done := make(chan struct{}, 1)
		c := dial(func(m string, _ json.RawMessage) {
			if m == rpc.MethodTurnDone {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
		for i := 0; i < turns; i++ {
			if _, _, err := c.Qua(ctx, fmt.Sprintf("turn %d", i), nil); err != nil {
				b.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				b.Fatal("turn never finished")
			}
		}
		c.Close()
	}
	return acli, created.FigaroID, dial, ctx
}

func BenchmarkDoorRoundTrip(b *testing.B) {
	_, _, dial, ctx := benchAria(b, 0)
	c := dial(nil)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Metrics(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoorListenCatchUp(b *testing.B) {
	_, _, dial, ctx := benchAria(b, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := dial(func(string, json.RawMessage) {})
		if _, err := c.ReadBefore(ctx, aria.Anchor{}, 64*1024); err != nil {
			b.Fatal(err)
		}
		c.Close()
	}
}

func BenchmarkDoorColdAttach(b *testing.B) {
	quietBench(b)
	a, acli, ctx := daemonFixture(b)
	ids := make([]string, 64)
	for i := range ids {
		created, err := acli.Create(ctx, dress(b, "mock"), nil)
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = created.FigaroID
		if err := a.Registry.Hibernate(created.FigaroID); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ac, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
		if err != nil {
			b.Fatal(err)
		}
		att, err := ac.Attach(ctx, ids[i%len(ids)])
		if err != nil {
			b.Fatal(err)
		}
		c, err := sdk.DialAria(ariaAddr(a, att.FigaroID), nil)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := c.Form(ctx); err != nil {
			b.Fatal(err)
		}
		c.Close()
		ac.Close()
	}
}

// BenchmarkDoorFollowerMemory reports the heap one following connection
// holds, client and daemon halves together, over 256 connections.
func BenchmarkDoorFollowerMemory(b *testing.B) {
	_, _, dial, _ := benchAria(b, 0)
	const conns = 256
	var per float64
	for i := 0; i < b.N; i++ {
		before := heapInuse()
		held := make([]*sdk.Aria, conns)
		for j := range held {
			held[j] = dial(func(string, json.RawMessage) {})
		}
		after := heapInuse()
		per = float64(after-before) / conns
		for _, c := range held {
			c.Close()
		}
	}
	b.ReportMetric(per, "B/conn")
}

func heapInuse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}
