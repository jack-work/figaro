package wirelog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type cannedTripper struct{ body []byte }

func (c cannedTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(c.body)), Request: req}, nil
}

// One provider round as the transport sees it: a 200 KB request body and a
// 64 KB SSE response read in 256-byte chunks, under a recording span.
func BenchmarkTransportStreamedRound(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(trace.NewSimpleSpanProcessor(tracetest.NewInMemoryExporter())))
	defer tp.Shutdown(context.Background())
	tr := &Transport{Inner: cannedTripper{body: bytes.Repeat([]byte("data: {}\n\n"), 6554)}}
	reqBody := strings.Repeat("x", 200<<10)
	buf := make([]byte, 256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, span := tp.Tracer("bench").Start(context.Background(), "round")
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid/v1/messages", strings.NewReader(reqBody))
		req.ContentLength = -1
		resp, err := tr.RoundTrip(req)
		if err != nil {
			b.Fatal(err)
		}
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				break
			}
		}
		resp.Body.Close()
		span.End()
	}
}
