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
	"go.opentelemetry.io/otel/codes"
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

// telemetryFileMax caps each telemetry .jsonl file; at most 2x that lives on
// disk (the active file + one rolled-over ".1"). Keeps the file exporter: the
// default when no OTLP agent is configured: from growing without bound in a
// long-lived daemon.
const telemetryFileMax = 16 << 20 // 16 MiB

// rotatingWriter is a size-capped append writer for the file exporters: when a
// write would push the file past maxBytes it rolls the file over (current →
// path+".1", fresh file started). Writes are serialized (the batch/periodic
// exporters write from one goroutine, but the lock keeps it correct if that
// changes). Implements io.Writer + io.Closer.
type rotatingWriter struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
	f        *os.File
	size     int64
}

func newRotatingWriter(path string, maxBytes int64) (*rotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	var size int64
	if fi, serr := f.Stat(); serr == nil {
		size = fi.Size()
	}
	return &rotatingWriter{path: path, maxBytes: maxBytes, f: f, size: size}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	_ = os.Rename(w.path, w.path+".1") // keep one previous generation
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

var (
	requestDuration otelmetric.Float64Histogram

	// The form/patch read path, as a PAIR: what a range read returned, and
	// how long the history it was drawn from was. A duration tells you it got
	// slow; the pair tells you whether a bounded read is still bounded, which
	// is the failure mode this path can actually have in production.
	//
	// Histograms rather than counters, because the tail is the bug: a mean of
	// 1.2 patches returned hides the one Send that walked forty thousand.
	formPatchesReturned otelmetric.Int64Histogram
	formPatchesHistory  otelmetric.Int64Histogram

	// The durability pair. syncDuration is how long an fsync took;
	// syncBatch is how many patches it covered. Group commit is the only
	// reason a mandatory sync is affordable, and a batch distribution
	// collapsing to 1 is the alarm that it stopped working.
	syncDuration otelmetric.Float64Histogram
	syncBatch    otelmetric.Int64Histogram

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

// newSpanProcessor picks how spans reach the file exporter. The long-lived
// daemon (_FIGARO_DAEMON=1) batches, so ending a span never blocks a turn on
// file I/O. A short-lived CLI process uses the simple (synchronous) processor:
// its span count is tiny so the cost is negligible, and it flushes on span end
// : batching there would silently drop spans on the os.Exit / die() paths that
// skip the deferred shutdown flush.
func newSpanProcessor(exp sdktrace.SpanExporter) sdktrace.SpanProcessor {
	if os.Getenv("_FIGARO_DAEMON") == "1" {
		return sdktrace.NewBatchSpanProcessor(exp)
	}
	return sdktrace.NewSimpleSpanProcessor(exp)
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

	traceFile, err := newRotatingWriter(filepath.Join(dir, "traces.jsonl"), telemetryFileMax)
	if err != nil {
		return nil, fmt.Errorf("open traces: %w", err)
	}
	traceExp, err := stdouttrace.New(stdouttrace.WithWriter(traceFile))
	if err != nil {
		traceFile.Close()
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(newSpanProcessor(traceExp)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	logFile, err := newRotatingWriter(filepath.Join(dir, "logs.jsonl"), telemetryFileMax)
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

	metricFile, err := newRotatingWriter(filepath.Join(dir, "metrics.jsonl"), telemetryFileMax)
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
		formPatchesReturned, err = m.Int64Histogram(
			"figaro.form.patches.returned",
			otelmetric.WithUnit("{patch}"),
			otelmetric.WithDescription("Form patches handed to one range read: the delta actually rendered"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "form.patches.returned", "err", err)
		}
		syncDuration, err = m.Float64Histogram(
			"figaro.wal.sync.duration",
			otelmetric.WithUnit("ms"),
			otelmetric.WithDescription("One fsync on a WAL channel, before anything it covers is published"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "wal.sync.duration", "err", err)
		}
		syncBatch, err = m.Int64Histogram(
			"figaro.wal.sync.batch",
			otelmetric.WithUnit("{patch}"),
			otelmetric.WithDescription("How many patches one fsync covered: the distribution that says whether group commit is working"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "wal.sync.batch", "err", err)
		}
		formPatchesHistory, err = m.Int64Histogram(
			"figaro.form.patches.history",
			otelmetric.WithUnit("{patch}"),
			otelmetric.WithDescription("The form's whole patch history at the moment of a range read: what the old copy-everything read paid, and the alarm if returned ever starts tracking it"),
		)
		if err != nil {
			slog.Warn("metric init", "name", "form.patches.history", "err", err)
		}
	})
}

// RecordSync records one fsync and how many patches it made durable.
func RecordSync(ctx context.Context, d time.Duration, patches int, attrs ...attribute.KeyValue) {
	if syncDuration != nil {
		syncDuration.Record(ctx, float64(d.Microseconds())/1000, otelmetric.WithAttributes(attrs...))
	}
	if syncBatch != nil {
		syncBatch.Record(ctx, int64(patches), otelmetric.WithAttributes(attrs...))
	}
}

// RecordFormPatchRead records one form patch-range read: how many patches the
// caller got, and how long the history it was drawn from was.
//
// The PAIR is the point. Before the view, a read returned one patch and copied
// the entire history, and no instrument in the binary could say so: the only
// metric figaro had was request duration, and a copy hides comfortably inside
// a network round trip. Returned-versus-history makes the difference a number,
// and a returned distribution that starts tracking history is the regression
// alarm for anything built on this path later.
func RecordFormPatchRead(ctx context.Context, returned, history int, attrs ...attribute.KeyValue) {
	if formPatchesReturned != nil {
		formPatchesReturned.Record(ctx, int64(returned), otelmetric.WithAttributes(attrs...))
	}
	if formPatchesHistory != nil {
		formPatchesHistory.Record(ctx, int64(history), otelmetric.WithAttributes(attrs...))
	}
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

// RecordError attaches an error to the span in ctx and flips its status
// to Error with the given description. Also emits a span event named
// `name` carrying the supplied attributes plus the error string, so the
// failure is greppable in traces.jsonl even before consulting Status.
// No-op if no active span.
func RecordError(ctx context.Context, name string, err error, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	all := append([]attribute.KeyValue{attribute.String("error", err.Error())}, attrs...)
	span.AddEvent(name, trace.WithAttributes(all...))
	span.RecordError(err, trace.WithAttributes(all...))
	span.SetStatus(codes.Error, name)
}

// RecordRequestDuration records a request roundtrip.
func RecordRequestDuration(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	if requestDuration == nil {
		return
	}
	requestDuration.Record(ctx, float64(d.Milliseconds()), otelmetric.WithAttributes(attrs...))
}
