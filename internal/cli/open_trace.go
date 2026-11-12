package cli

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

var (
	processStarted atomic.Pointer[time.Time]
	pendingOpen    atomic.Pointer[trace.Span]
)

func startOpen(ctx context.Context, figaroID string) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attribute.String("figaro.id", figaroID))}
	if at := processStarted.Load(); at != nil {
		opts = append(opts, trace.WithTimestamp(*at))
	}
	_, span := figOtel.Start(ctx, "cli.open", opts...)
	if old := pendingOpen.Swap(&span); old != nil {
		(*old).End()
	}
}

func endOpen(painted bool) {
	if span := pendingOpen.Swap(nil); span != nil {
		(*span).SetAttributes(attribute.Bool("cli.painted", painted))
		(*span).End()
	}
}
