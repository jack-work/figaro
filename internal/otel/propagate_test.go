package otel_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

func TestStartFromRemoteParentsOnTheFirstAndLinksTheRest(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	client, cspan := figOtel.Start(context.Background(), "client")
	first := figOtel.TraceParent(client)
	cspan.End()
	if first == "" {
		t.Fatal("no traceparent from a recording span")
	}
	second := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	_, span := figOtel.StartFromRemote(context.Background(), "turn", []string{"garbage", first, second})
	span.End()

	got := rec.Ended()[1]
	if got.Parent().TraceID() != cspan.SpanContext().TraceID() || got.Parent().SpanID() != cspan.SpanContext().SpanID() {
		t.Errorf("parent = %v, want the client span", got.Parent())
	}
	if len(got.Links()) != 1 || got.Links()[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("links = %v", got.Links())
	}
	if figOtel.TraceParent(context.Background()) != "" {
		t.Error("a context without a span has no traceparent")
	}
}
