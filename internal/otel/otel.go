// Package otel wraps OpenTelemetry SDK init, tracing, and metrics.
package otel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "figaro"

var (
	requestDuration otelmetric.Float64Histogram
	toolCallCounter otelmetric.Int64Counter
	instrumentsOnce sync.Once
)

// envLogLevel resolves FIGARO_LOG_LEVEL into a slog level. Defaults to INFO.
func envLogLevel() slog.Level {
	switch strings.ToLower(os.Getenv("FIGARO_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// leveledHandler filters by slog level on top of the otelslog bridge.
type leveledHandler struct {
	inner slog.Handler
	level slog.Level
}

func (h *leveledHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}
func (h *leveledHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}
func (h *leveledHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &leveledHandler{inner: h.inner.WithAttrs(attrs), level: h.level}
}
func (h *leveledHandler) WithGroup(name string) slog.Handler {
	return &leveledHandler{inner: h.inner.WithGroup(name), level: h.level}
}

// Init wires OTel providers writing to dir. Installs slog.Default().
func Init(ctx context.Context, dir string) (func(context.Context) error, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String("figaro"),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	traceFile, err := os.OpenFile(filepath.Join(dir, "traces.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open traces: %w", err)
	}
	traceExp, err := stdouttrace.New(stdouttrace.WithWriter(traceFile))
	if err != nil {
		traceFile.Close()
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(traceExp)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	logFile, err := os.OpenFile(filepath.Join(dir, "logs.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		traceFile.Close()
		return nil, fmt.Errorf("open logs: %w", err)
	}
	logExp, err := stdoutlog.New(stdoutlog.WithWriter(logFile))
	if err != nil {
		traceFile.Close()
		logFile.Close()
		return nil, fmt.Errorf("log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	otellogglobal.SetLoggerProvider(lp)
	bridge := otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp))
	slog.SetDefault(slog.New(&leveledHandler{inner: bridge, level: envLogLevel()}))

	metricFile, err := os.OpenFile(filepath.Join(dir, "metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		traceFile.Close()
		logFile.Close()
		return nil, fmt.Errorf("open metrics: %w", err)
	}
	metricExp, err := stdoutmetric.New(stdoutmetric.WithWriter(metricFile))
	if err != nil {
		traceFile.Close()
		logFile.Close()
		metricFile.Close()
		return nil, fmt.Errorf("metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	initInstruments(mp.Meter(scopeName))

	shutdown := func(ctx context.Context) error {
		var first error
		setFirst := func(err error) {
			if err != nil && first == nil {
				first = err
			}
		}
		setFirst(tp.Shutdown(ctx))
		setFirst(lp.Shutdown(ctx))
		setFirst(mp.Shutdown(ctx))
		traceFile.Close()
		logFile.Close()
		metricFile.Close()
		return first
	}
	return shutdown, nil
}

func initInstruments(m otelmetric.Meter) {
	instrumentsOnce.Do(func() {
		var err error
		requestDuration, err = m.Float64Histogram(
			"figaro.request.duration",
			otelmetric.WithUnit("ms"),
			otelmetric.WithDescription("Provider request roundtrip latency"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "request.duration", "err", err)
		}
		toolCallCounter, err = m.Int64Counter(
			"figaro.tool.calls",
			otelmetric.WithDescription("Tool dispatches by status"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "tool.calls", "err", err)
		}
	})
}

// Tracer returns the figaro tracer.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(scopeName)
}

// Start begins a new span. Shorthand for Tracer().Start(ctx, name).
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// WithAttributes returns a SpanStartOption that sets attributes on the span.
func WithAttributes(attrs ...attribute.KeyValue) trace.SpanStartOption {
	return trace.WithAttributes(attrs...)
}

// Event records an event on the span in ctx. No-op if no active span.
func Event(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// RecordRequestDuration records a request roundtrip.
func RecordRequestDuration(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	if requestDuration == nil {
		return
	}
	requestDuration.Record(ctx, float64(d.Milliseconds()), otelmetric.WithAttributes(attrs...))
}

// RecordToolCall counts a tool dispatch outcome.
func RecordToolCall(ctx context.Context, status string, attrs ...attribute.KeyValue) {
	if toolCallCounter == nil {
		return
	}
	all := append([]attribute.KeyValue{attribute.String("status", status)}, attrs...)
	toolCallCounter.Add(ctx, 1, otelmetric.WithAttributes(all...))
}
