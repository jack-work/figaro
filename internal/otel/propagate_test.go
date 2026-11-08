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

func TestValidTraceParentTakesOnlyVersion00WithRealIDs(t *testing.T) {
	ok := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if !figOtel.ValidTraceParent(ok) {
		t.Fatal("a W3C version 00 traceparent was refused")
	}
	for _, bad := range []string{
		"", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		ok + "-x",
	} {
		if figOtel.ValidTraceParent(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestStartFromRemoteIgnoresAMalformedTraceparent(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)
	_, span := figOtel.StartFromRemote(context.Background(), "turn", []string{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"})
	span.End()
	if got := rec.Ended()[0]; got.Parent().IsValid() || got.SpanContext().TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("a malformed traceparent parented the turn")
	}
}
