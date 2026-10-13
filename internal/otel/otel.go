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

	"github.com/jack-work/figaro/internal/logring"
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

// telemetryFileMax caps each telemetry .jsonl file; the active file plus one
// rolled-over ".1" is all that lives on disk.
const telemetryFileMax = 16 << 20

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
	_ = os.Rename(w.path, w.path+".1")
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

	// returned against history says whether a bounded read is still bounded.
	formPatchesReturned otelmetric.Int64Histogram
	formPatchesHistory  otelmetric.Int64Histogram

	// A syncBatch distribution collapsing to 1 means group commit stopped.
	syncDuration otelmetric.Float64Histogram
	syncBatch    otelmetric.Int64Histogram

	providerTTFT otelmetric.Float64Histogram
	turnDuration otelmetric.Float64Histogram
	turnRounds   otelmetric.Int64Histogram
	toolDuration otelmetric.Float64Histogram

	catchUpDuration otelmetric.Float64Histogram
	catchUpVisited  otelmetric.Int64Histogram
	catchUpEncoded  otelmetric.Int64Histogram

	tokens otelmetric.Int64Counter

	rpcDuration otelmetric.Float64Histogram

	instrumentsOnce sync.Once

	turnMS = []float64{250, 500, 1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000}
	subMS  = []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}
)

func envLogLevel() slog.Level {
	lvl := os.Getenv("FIGARO_LOG_LEVEL")
	if lvl == "" {
		lvl = configuredLevel
	}
	switch strings.ToLower(lvl) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

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

// The daemon batches so a span end never blocks a turn; the CLI exports
// synchronously because os.Exit paths skip the deferred flush.
func newSpanProcessor(exp sdktrace.SpanExporter) sdktrace.SpanProcessor {
	if os.Getenv("_FIGARO_DAEMON") == "1" {
		return sdktrace.NewBatchSpanProcessor(exp)
	}
	return sdktrace.NewSimpleSpanProcessor(exp)
}

// configuredLevel is the [telemetry] level from config; FIGARO_LOG_LEVEL
// overrides it.
var configuredLevel string

// SetConfiguredLevel must be called before Init.
func SetConfiguredLevel(level string) { configuredLevel = level }

// The file sink exports inline: a queue before it retains records at idle.
const fileQueue = 0

// Init wires OTel providers writing to dir and installs slog.Default().
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
		sdklog.WithProcessor(newLogProcessor(logExp, fileQueue)),
		sdklog.WithResource(res),
	)
	otellogglobal.SetLoggerProvider(lp)
	bridge := otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp))
	// The ring retains in addition to exporting, never instead.
	ring := logring.New(&leveledHandler{inner: bridge, level: envLogLevel()},
		logring.DefaultCapacity,
		logring.Any(
			logring.AtLeast(slog.LevelWarn),
			logring.WithMessage(retainedMessages...),
		))
	setRecent(ring)
	slog.SetDefault(slog.New(ring))

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
			otelmetric.WithExplicitBucketBoundaries(turnMS...),
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
		providerTTFT = histogram(m, "figaro.provider.ttft", "Round start to the first content delta", turnMS)
		turnDuration = histogram(m, "figaro.turn.duration", "One user turn: every round and tool call", turnMS)
		turnRounds = counts(m, "figaro.turn.rounds", "{round}", "Provider rounds one turn took")
		toolDuration = histogram(m, "figaro.tool.duration", "One tool call, Execute to return", subMS)
		catchUpDuration = histogram(m, "figaro.translator.catchup", "One translator catch-up before a send", subMS)
		catchUpVisited = counts(m, "figaro.translator.catchup.visited", "{record}", "Fig IR records at or after the watermark")
		catchUpEncoded = counts(m, "figaro.translator.catchup.encoded", "{record}", "Records translated and written; visited far above this is the O(delta) alarm")
		rpcDuration = histogram(m, "figaro.rpc.duration", "One daemon-side RPC handler, by method", subMS)
		tokens, err = m.Int64Counter("figaro.tokens", otelmetric.WithUnit("{token}"),
			otelmetric.WithDescription("Tokens by kind: input, output, cache_read, cache_write"))
		if err != nil {
			slog.Warn("metric init", "name", "tokens", "err", err)
		}
	})
}

func histogram(m otelmetric.Meter, name, desc string, bounds []float64) otelmetric.Float64Histogram {
	h, err := m.Float64Histogram(name, otelmetric.WithUnit("ms"), otelmetric.WithDescription(desc),
		otelmetric.WithExplicitBucketBoundaries(bounds...))
	if err != nil {
		slog.Warn("metric init", "name", name, "err", err)
	}
	return h
}

func counts(m otelmetric.Meter, name, unit, desc string) otelmetric.Int64Histogram {
	h, err := m.Int64Histogram(name, otelmetric.WithUnit(unit), otelmetric.WithDescription(desc))
	if err != nil {
		slog.Warn("metric init", "name", name, "err", err)
	}
	return h
}

func record(ctx context.Context, h otelmetric.Float64Histogram, d time.Duration, attrs ...attribute.KeyValue) {
	if h != nil {
		h.Record(ctx, float64(d.Microseconds())/1000, otelmetric.WithAttributes(attrs...))
	}
}

func recordN(ctx context.Context, h otelmetric.Int64Histogram, n int64, attrs ...attribute.KeyValue) {
	if h != nil {
		h.Record(ctx, n, otelmetric.WithAttributes(attrs...))
	}
}

func RecordTTFT(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	record(ctx, providerTTFT, d, attrs...)
}

func RecordTurn(ctx context.Context, d time.Duration, rounds int, attrs ...attribute.KeyValue) {
	record(ctx, turnDuration, d, attrs...)
	recordN(ctx, turnRounds, int64(rounds), attrs...)
}

func RecordTool(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	record(ctx, toolDuration, d, attrs...)
}

func RecordCatchUp(ctx context.Context, d time.Duration, visited, encoded int, attrs ...attribute.KeyValue) {
	record(ctx, catchUpDuration, d, attrs...)
	recordN(ctx, catchUpVisited, int64(visited), attrs...)
	recordN(ctx, catchUpEncoded, int64(encoded), attrs...)
}

func AddTokens(ctx context.Context, in, out, cacheRead, cacheWrite int, attrs ...attribute.KeyValue) {
	if tokens == nil {
		return
	}
	for kind, n := range map[string]int{"input": in, "output": out, "cache_read": cacheRead, "cache_write": cacheWrite} {
		tokens.Add(ctx, int64(n), otelmetric.WithAttributes(append(attrs, attribute.String("kind", kind))...))
	}
}

func RecordRPC(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	record(ctx, rpcDuration, d, attrs...)
}

func RecordSync(ctx context.Context, d time.Duration, patches int, attrs ...attribute.KeyValue) {
	if syncDuration != nil {
		syncDuration.Record(ctx, float64(d.Microseconds())/1000, otelmetric.WithAttributes(attrs...))
	}
	if syncBatch != nil {
		syncBatch.Record(ctx, int64(patches), otelmetric.WithAttributes(attrs...))
	}
}

func RecordFormPatchRead(ctx context.Context, returned, history int, attrs ...attribute.KeyValue) {
	if formPatchesReturned != nil {
		formPatchesReturned.Record(ctx, int64(returned), otelmetric.WithAttributes(attrs...))
	}
	if formPatchesHistory != nil {
		formPatchesHistory.Record(ctx, int64(history), otelmetric.WithAttributes(attrs...))
	}
}

func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(scopeName)
}

func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

func WithAttributes(attrs ...attribute.KeyValue) trace.SpanStartOption {
	return trace.WithAttributes(attrs...)
}

// Event is a no-op without an active span.
func Event(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// RecordError sets the span status to Error and also emits a span event
// named `name`, so the failure is greppable in traces.jsonl.
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

func RecordRequestDuration(ctx context.Context, d time.Duration, attrs ...attribute.KeyValue) {
	if requestDuration == nil {
		return
	}
	requestDuration.Record(ctx, float64(d.Milliseconds()), otelmetric.WithAttributes(attrs...))
}

// retainedMessages are kept in the ring regardless of level. Keep the list
// short: it is the memory bound.
var retainedMessages = []string{
	"provider round-trip",
}

var (
	recentMu sync.Mutex
	recent   *logring.Ring
)

func setRecent(r *logring.Ring) {
	recentMu.Lock()
	recent = r
	recentMu.Unlock()
}

// Recent is nil before Init; callers must tolerate that.
func Recent() *logring.Ring {
	recentMu.Lock()
	defer recentMu.Unlock()
	return recent
}

func SetRecentForTest(r *logring.Ring) { setRecent(r) }
