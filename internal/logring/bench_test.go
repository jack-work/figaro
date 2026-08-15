package logring

import (
	"io"
	"log/slog"
	"testing"
)

// The two numbers that decide whether this is affordable.
//
// NotRetained is the common case - a log line the policy does not keep - and
// it must cost nothing beyond the predicate. Measured: 0 B/op, 0 allocs/op;
// the nanoseconds are the inner JSON handler's, not ours.
//
// Retained is what a kept record costs: ~500 B and ~950ns over the baseline,
// for the mandatory copy of the attributes (a Handler may not retain a
// Record). At the default capacity of 512 that bounds the ring at roughly a
// quarter of a megabyte, which `figaro doctor mem` reports next to a 32 MiB
// segment cache.

func BenchmarkHandleNotRetained(b *testing.B) {
	ring := New(slog.NewJSONHandler(io.Discard, nil), DefaultCapacity, AtLeast(slog.LevelWarn))
	log := slog.New(ring)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Info("log opened", "dir", "/some/path", "codec", "jsonl", "sealed", 0)
	}
}

func BenchmarkHandleRetained(b *testing.B) {
	ring := New(slog.NewJSONHandler(io.Discard, nil), DefaultCapacity, AtLeast(slog.LevelInfo))
	log := slog.New(ring)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Info("provider round-trip", "aria", "94f0752b", "method", "POST",
			"url", "https://api.anthropic.com/v1/messages", "duration_ms", 11015,
			"req_bytes", 729498, "status", 200, "request_id", "req_011Ce3TTTY6NjCj66TBPgp3K")
	}
}
