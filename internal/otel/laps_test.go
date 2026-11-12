package otel

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Laps taken before the exporter exists must still land as children that
// tile the parent, with their own start and end times.
func TestLapsExportPhasesTakenBeforeTheTracer(t *testing.T) {
	laps := StartLaps("daemon.boot")
	time.Sleep(2 * time.Millisecond)
	laps.Lap("config")
	time.Sleep(2 * time.Millisecond)
	laps.Lap("store_open")

	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)
	laps.End(context.Background())

	ended := rec.Ended()
	if len(ended) != 3 {
		t.Fatalf("want parent and 2 phases, got %d spans", len(ended))
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range ended {
		byName[s.Name()] = s
	}
	parent, cfg, open := byName["daemon.boot"], byName["daemon.boot.config"], byName["daemon.boot.store_open"]
	if parent == nil || cfg == nil || open == nil {
		t.Fatalf("missing spans: %v", byName)
	}
	for _, c := range []sdktrace.ReadOnlySpan{cfg, open} {
		if c.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("%s is not a child of daemon.boot", c.Name())
		}
	}
	if !cfg.StartTime().Equal(parent.StartTime()) || !cfg.EndTime().Equal(open.StartTime()) || !open.EndTime().Equal(parent.EndTime()) {
		t.Errorf("phases do not tile the parent: parent %v..%v config %v..%v store_open %v..%v",
			parent.StartTime(), parent.EndTime(), cfg.StartTime(), cfg.EndTime(), open.StartTime(), open.EndTime())
	}
	if d := cfg.EndTime().Sub(cfg.StartTime()); d < 2*time.Millisecond {
		t.Errorf("config phase lasted %v, want at least the 2ms slept", d)
	}
}

func TestNilLapsIsInert(t *testing.T) {
	var laps *Laps
	laps.Lap("x")
	laps.End(context.Background())
}
