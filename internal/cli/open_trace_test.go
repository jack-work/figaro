package cli

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// cli.open runs from process start to the first painted content, once.
func TestOpenSpanRunsFromProcessStartToFirstPaintOnce(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)
	prevStart := processStarted.Load()
	defer processStarted.Store(prevStart)

	started := time.Now().Add(-time.Second)
	processStarted.Store(&started)
	startOpen(context.Background(), "abc")
	endOpen(true)
	endOpen(false)

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("want one cli.open, got %d spans", len(ended))
	}
	s := ended[0]
	if s.Name() != "cli.open" || !s.StartTime().Equal(started) {
		t.Fatalf("got %s starting %v, want cli.open at process start %v", s.Name(), s.StartTime(), started)
	}
	attrs := map[string]any{}
	for _, a := range s.Attributes() {
		attrs[string(a.Key)] = a.Value.AsInterface()
	}
	if attrs["cli.painted"] != true || attrs["figaro.id"] != "abc" {
		t.Fatalf("attrs %v", attrs)
	}
}
