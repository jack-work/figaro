// Package otel provides OpenTelemetry initialization for figaro.
//
// For now, traces are exported to a JSONL file. Later this can be
// swapped to OTLP without changing call sites.
//
// Usage:
//
//	shutdown, err := otel.Init(ctx, "~/.local/state/figaro/traces.jsonl")
//	defer shutdown(ctx)
//
//	ctx, span := otel.Start(ctx, "figaro.prompt")
//	defer span.End()
package otel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "figaro"

// Init sets up the global tracer provider with a file exporter.
// Returns a shutdown function that flushes pending spans.
// The trace file is created (with parent dirs) if it doesn't exist.
func Init(ctx context.Context, traceFile string) (func(context.Context) error, error) {
	if err := os.MkdirAll(filepath.Dir(traceFile), 0700); err != nil {
		return nil, fmt.Errorf("create trace dir: %w", err)
	}

	f, err := os.OpenFile(traceFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open trace file: %w", err)
	}

	exporter, err := stdouttrace.New(stdouttrace.WithWriter(f))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("create exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("figaro"),
		),
	)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error {
		err := tp.Shutdown(ctx)
		f.Close()
		return err
	}

	return shutdown, nil
}

// Tracer returns the figaro tracer. Use this for creating spans.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(tracerName)
}

// Start begins a new span. Shorthand for Tracer().Start(ctx, name).
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// WithAttributes returns a SpanStartOption that sets attributes on the span.
func WithAttributes(attrs ...attribute.KeyValue) trace.SpanStartOption {
	return trace.WithAttributes(attrs...)
}
