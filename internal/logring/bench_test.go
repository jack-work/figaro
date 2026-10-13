package logring

import (
	"io"
	"log/slog"
	"testing"
)

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
