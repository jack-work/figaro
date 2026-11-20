package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/internal/wirelog"
)

const Rou2UpstreamHeader = wirelog.Rou2UpstreamHeader

func Rou2Upstream(h http.Header) (string, bool) { return wirelog.Rou2Upstream(h) }

func RecordRou2Upstream(ctx context.Context, h http.Header) (string, bool) {
	upstream, ok := Rou2Upstream(h)
	if ok {
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(attribute.String("rou2.upstream", upstream))
		}
	}
	return upstream, ok
}

type RateLimited struct {
	Provider string
	Upstream string
	Window   string
	Resets   time.Time
}

func (e *RateLimited) Error() string {
	limit := "rate limit"
	if e.Window != "" {
		limit = e.Window + " rate limit"
	}
	who := e.Provider
	if e.Upstream != "" {
		who += " (" + e.Upstream + ")"
	}
	msg := fmt.Sprintf("%s: %s reached", who, limit)
	if !e.Resets.IsZero() {
		msg += "; resets " + e.Resets.Local().Format("Mon 2 Jan 15:04 MST")
	}
	return msg
}
