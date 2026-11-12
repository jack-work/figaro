package otel

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/internal/mark"
)

type Laps struct {
	name  string
	start time.Time
	last  time.Time
	laps  []lap
}

type lap struct {
	name     string
	from, to time.Time
}

func StartLaps(name string) *Laps {
	now := time.Now()
	return &Laps{name: name, start: now, last: now}
}

func (l *Laps) Started() time.Time {
	if l == nil {
		return time.Time{}
	}
	return l.start
}

func (l *Laps) Lap(name string) {
	if l == nil {
		return
	}
	now := time.Now()
	l.laps = append(l.laps, lap{name: name, from: l.last, to: now})
	mark.Mark(l.name+"."+name, "ms", float64(now.Sub(l.last).Microseconds())/1000)
	l.last = now
}

func (l *Laps) End(ctx context.Context, attrs ...attribute.KeyValue) {
	if l == nil {
		return
	}
	mark.Mark(l.name, "ms", float64(l.last.Sub(l.start).Microseconds())/1000)
	ctx, span := Tracer().Start(ctx, l.name, trace.WithTimestamp(l.start), trace.WithAttributes(attrs...))
	for _, p := range l.laps {
		_, s := Tracer().Start(ctx, l.name+"."+p.name, trace.WithTimestamp(p.from))
		s.End(trace.WithTimestamp(p.to))
	}
	span.End(trace.WithTimestamp(l.last))
}
