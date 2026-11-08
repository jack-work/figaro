package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var traceContext = propagation.TraceContext{}

func TraceParent(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	traceContext.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

func remoteSpanContext(traceparent string) trace.SpanContext {
	if traceparent == "" {
		return trace.SpanContext{}
	}
	ctx := traceContext.Extract(context.Background(), propagation.MapCarrier{"traceparent": traceparent})
	return trace.SpanContextFromContext(ctx)
}

func StartFromRemote(ctx context.Context, name string, traceparents []string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	parented := false
	for _, tp := range traceparents {
		sc := remoteSpanContext(tp)
		if !sc.IsValid() {
			continue
		}
		if !parented {
			ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
			parented = true
			continue
		}
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: sc}))
	}
	return Tracer().Start(ctx, name, opts...)
}

func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(attrs...)
	}
}
