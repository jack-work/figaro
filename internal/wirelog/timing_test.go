package wirelog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Upload, headers, first byte and body end are the four marks the collector
// splits a round's wait into; a streamed body has no length up front.
func TestTransportMarksUploadFirstByteAndBodyEnd(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "round")

	tr := &Transport{Inner: cannedTripper{body: []byte("data: one\n\ndata: two\n\n")}}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid/v1/messages", io.NopCloser(strings.NewReader("hello body")))
	req.ContentLength = -1
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	span.End()

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d", len(ended))
	}
	var order []string
	vals := map[string]int64{}
	for _, e := range ended[0].Events() {
		order = append(order, e.Name)
		for _, kv := range e.Attributes {
			if kv.Key == "http.req_bytes" || kv.Key == "http.resp_bytes" {
				vals[e.Name+"/"+string(kv.Key)] = kv.Value.AsInt64()
			}
		}
	}
	want := []string{"http.body_sent", "http.request", "http.first_byte", "http.body_done"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", order, want)
	}
	if vals["http.body_sent/http.req_bytes"] != 10 || vals["http.request/http.req_bytes"] != 10 {
		t.Errorf("req bytes = %v", vals)
	}
	if vals["http.body_done/http.resp_bytes"] != 22 {
		t.Errorf("resp bytes = %v", vals)
	}
}
