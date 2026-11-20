package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const Rou2UpstreamHeader = "X-Rou2-Upstream"

var rou2UpstreamName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func Rou2Upstream(h http.Header) (string, bool) {
	v := strings.TrimSpace(h.Get(Rou2UpstreamHeader))
	return v, rou2UpstreamName.MatchString(v)
}

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
