package cli

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

type submittedAtKey struct{}

func withSubmittedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, submittedAtKey{}, at)
}

type pendingSubmit struct {
	span   trace.Span
	cursor uint64
	seen   bool
}

var submits = struct {
	sync.Mutex
	m       map[string]*pendingSubmit
	hook    sync.Once
	pending atomic.Int32
}{m: map[string]*pendingSubmit{}}

func startSubmit(ctx context.Context, figaroID string, promptBytes int) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(
		attribute.String("figaro.id", figaroID),
		attribute.Int("cli.prompt_bytes", promptBytes),
	)}
	if at, ok := ctx.Value(submittedAtKey{}).(time.Time); ok {
		opts = append(opts, trace.WithTimestamp(at))
	}
	return figOtel.Tracer().Start(ctx, "cli.submit", opts...)
}

func awaitFirstPaint(figaroID string, cursor int, span trace.Span) {
	if !span.IsRecording() {
		span.End()
		return
	}
	submits.hook.Do(func() { atExit(endUnpaintedSubmits) })
	submits.Lock()
	prev := submits.m[figaroID]
	submits.m[figaroID] = &pendingSubmit{span: span, cursor: uint64(max(cursor, 0))}
	submits.pending.Store(int32(len(submits.m)))
	submits.Unlock()
	if prev != nil {
		prev.span.SetAttributes(attribute.Bool("cli.painted", false))
		prev.span.End()
	}
}

func submitFrame(figaroID string, page aria.Page) {
	if submits.pending.Load() == 0 {
		return
	}
	submits.Lock()
	defer submits.Unlock()
	p := submits.m[figaroID]
	if p == nil || p.seen || !pageCarriesReply(page, p.cursor) {
		return
	}
	p.seen = true
	p.span.AddEvent("reply.first_frame")
}

func submitPainted(figaroID string) {
	if submits.pending.Load() == 0 {
		return
	}
	submits.Lock()
	p := submits.m[figaroID]
	if p == nil || !p.seen {
		submits.Unlock()
		return
	}
	delete(submits.m, figaroID)
	submits.pending.Store(int32(len(submits.m)))
	submits.Unlock()
	p.span.AddEvent("reply.first_paint")
	p.span.SetAttributes(attribute.Bool("cli.painted", true))
	p.span.End()
}

func endUnpaintedSubmits() {
	submits.Lock()
	pending := submits.m
	submits.m = map[string]*pendingSubmit{}
	submits.pending.Store(0)
	submits.Unlock()
	for _, p := range pending {
		p.span.SetAttributes(attribute.Bool("cli.painted", false))
		p.span.End()
	}
}

func pageCarriesReply(page aria.Page, cursor uint64) bool {
	for _, part := range page.Parts {
		if part.ID <= cursor {
			continue
		}
		if part.Live != nil && len(part.Live.Nodes) > 0 {
			return true
		}
		for _, n := range part.Nodes {
			if n.Role != livedoc.RoleInput {
				return true
			}
		}
	}
	return false
}
