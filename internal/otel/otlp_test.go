package otel_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

type otlpSink struct {
	mu    sync.Mutex
	paths map[string]int
}

func (s *otlpSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	s.mu.Lock()
	s.paths[r.URL.Path]++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func (s *otlpSink) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[path]
}

func TestOTLPEndpointCarriesTracesLogsAndMetrics(t *testing.T) {
	sink := &otlpSink{paths: map[string]int{}}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	figOtel.SetOTLPEndpoint(srv.URL)
	defer figOtel.SetOTLPEndpoint("")
	prev := slog.Default()
	defer slog.SetDefault(prev)

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, t.TempDir())
	require.NoError(t, err)

	_, span := figOtel.Start(ctx, "test.otlp")
	span.End()
	slog.Info("otlp test record")
	figOtel.RecordRPC(ctx, time.Millisecond)

	require.NoError(t, shutdown(ctx))
	assert.Positive(t, sink.count("/v1/traces"), "traces")
	assert.Positive(t, sink.count("/v1/logs"), "logs")
	assert.Positive(t, sink.count("/v1/metrics"), "metrics")
}

func TestOTLPEndpointDownNeitherFailsInitNorHangsShutdown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().String()
	srv.Close()

	figOtel.SetOTLPEndpoint(addr)
	defer figOtel.SetOTLPEndpoint("")
	prev := slog.Default()
	defer slog.SetDefault(prev)

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, t.TempDir())
	require.NoError(t, err)
	_, span := figOtel.Start(ctx, "test.otlp.down")
	span.End()

	start := time.Now()
	_ = shutdown(ctx)
	assert.Less(t, time.Since(start), 8*time.Second)
}

// A collector that accepts the request and never answers held daemon exit,
// and the store lock with it, for the full 5 s export timeout while the next
// daemon waited. The files already hold every span and log line, so the exit
// flush gets ExitFlush and no more.
func TestShutdownAgainstACollectorThatNeverAnswersTakesTheExitFlushBudget(t *testing.T) {
	var reached atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	figOtel.SetOTLPEndpoint(srv.URL)
	defer figOtel.SetOTLPEndpoint("")
	prev := slog.Default()
	defer slog.SetDefault(prev)

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, t.TempDir())
	require.NoError(t, err)
	_, span := figOtel.Start(ctx, "test.otlp.wedged")
	span.End()
	slog.Info("otlp wedged record")
	figOtel.RecordRPC(ctx, time.Millisecond)

	start := time.Now()
	_ = shutdown(ctx)
	took := time.Since(start)

	require.Positive(t, reached.Load(), "the flush never reached the collector, so nothing was bounded")
	assert.GreaterOrEqual(t, took, figOtel.ExitFlush, "the flush gave up before its budget")
	assert.Less(t, took, 3*time.Second, "the next daemon waits on the store lock for as long as this takes")
}
