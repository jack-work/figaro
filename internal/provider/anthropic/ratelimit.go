package anthropic

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const unifiedRateLimitPrefix = "anthropic-ratelimit-unified-"

func recordUnifiedRateLimit(ctx context.Context, h http.Header) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	var attrs []attribute.KeyValue
	for name, values := range h {
		suffix, ok := strings.CutPrefix(strings.ToLower(name), unifiedRateLimitPrefix)
		if !ok || suffix == "" || len(values) == 0 {
			continue
		}
		if kv, ok := unifiedRateLimitAttr(suffix, strings.TrimSpace(values[0])); ok {
			attrs = append(attrs, kv)
		}
	}
	span.SetAttributes(attrs...)
}

func unifiedRateLimitAttr(suffix, v string) (attribute.KeyValue, bool) {
	key := attribute.Key("figaro.ratelimit." + strings.ReplaceAll(suffix, "-", "_"))
	switch {
	case strings.HasSuffix(suffix, "reset"):
		secs, err := strconv.ParseInt(v, 10, 64)
		return key.Int64(secs), err == nil
	case strings.HasSuffix(suffix, "utilization"), strings.HasSuffix(suffix, "percentage"), strings.HasSuffix(suffix, "threshold"):
		f, err := strconv.ParseFloat(v, 64)
		return key.Float64(f), err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return key.String(v), v != ""
}
