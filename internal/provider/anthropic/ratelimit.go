package anthropic

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/internal/provider"
)

const unifiedRateLimitPrefix = "anthropic-ratelimit-unified-"

type requestedModelKey struct{}

func withRequestedModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, requestedModelKey{}, model)
}

type rateLimitKind int

const (
	rateLimitText rateLimitKind = iota
	rateLimitUnixSeconds
	rateLimitFraction
)

var unifiedRateLimitSuffixes = []struct {
	suffix string
	kind   rateLimitKind
}{
	{"status", rateLimitText},
	{"reset", rateLimitUnixSeconds},
	{"representative-claim", rateLimitText},
	{"5h-status", rateLimitText},
	{"5h-reset", rateLimitUnixSeconds},
	{"5h-utilization", rateLimitFraction},
	{"7d-status", rateLimitText},
	{"7d-reset", rateLimitUnixSeconds},
	{"7d-utilization", rateLimitFraction},
	{"7d-surpassed-threshold", rateLimitFraction},
	{"fallback-percentage", rateLimitFraction},
	{"overage-status", rateLimitText},
	{"overage-disabled-reason", rateLimitText},
	{"upgrade-paths", rateLimitText},
}

func recordUnifiedRateLimit(ctx context.Context, h http.Header) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	var attrs []attribute.KeyValue
	for _, s := range unifiedRateLimitSuffixes {
		v := strings.TrimSpace(h.Get(unifiedRateLimitPrefix + s.suffix))
		if kv, ok := unifiedRateLimitAttr(s.suffix, s.kind, v); ok {
			attrs = append(attrs, kv)
		}
	}
	if _, ok := provider.RecordRou2Upstream(ctx, h); ok {
		if model, _ := ctx.Value(requestedModelKey{}).(string); model != "" {
			attrs = append(attrs, attribute.String("figaro.ratelimit.requested_model", model))
		}
	}
	span.SetAttributes(attrs...)
}

func unifiedRateLimitAttr(suffix string, kind rateLimitKind, v string) (attribute.KeyValue, bool) {
	key := attribute.Key("figaro.ratelimit." + strings.ReplaceAll(suffix, "-", "_"))
	switch kind {
	case rateLimitUnixSeconds:
		secs, err := strconv.ParseInt(v, 10, 64)
		return key.Int64(secs), err == nil
	case rateLimitFraction:
		f, err := strconv.ParseFloat(v, 64)
		return key.Float64(f), err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return key.String(v), v != ""
}
