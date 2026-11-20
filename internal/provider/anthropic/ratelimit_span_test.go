package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// The King throttles the fleet from these attributes on figaro.round, so they
// must land on every response that carries the headers, typed, and nothing
// else from the response may ride along. Values are from a recorded OAuth
// response.
var unifiedHeaders = map[string]string{
	"anthropic-ratelimit-unified-status":                  "allowed_warning",
	"anthropic-ratelimit-unified-reset":                   "1794880800",
	"anthropic-ratelimit-unified-representative-claim":    "seven_day",
	"anthropic-ratelimit-unified-5h-status":               "allowed",
	"anthropic-ratelimit-unified-5h-reset":                "1794556200",
	"anthropic-ratelimit-unified-5h-utilization":          "0.25",
	"anthropic-ratelimit-unified-7d-status":               "allowed_warning",
	"anthropic-ratelimit-unified-7d-reset":                "1794880800",
	"anthropic-ratelimit-unified-7d-utilization":          "0.92",
	"anthropic-ratelimit-unified-7d-surpassed-threshold":  "0.75",
	"anthropic-ratelimit-unified-fallback-percentage":     "0.5",
	"anthropic-ratelimit-unified-overage-status":          "rejected",
	"anthropic-ratelimit-unified-overage-disabled-reason": "out_of_credits",
	"anthropic-ratelimit-unified-upgrade-paths":           "max_20x",
}

var unifiedAttrs = map[attribute.Key]attribute.Value{
	"figaro.ratelimit.status":                  attribute.StringValue("allowed_warning"),
	"figaro.ratelimit.reset":                   attribute.Int64Value(1794880800),
	"figaro.ratelimit.representative_claim":    attribute.StringValue("seven_day"),
	"figaro.ratelimit.5h_status":               attribute.StringValue("allowed"),
	"figaro.ratelimit.5h_reset":                attribute.Int64Value(1794556200),
	"figaro.ratelimit.5h_utilization":          attribute.Float64Value(0.25),
	"figaro.ratelimit.7d_status":               attribute.StringValue("allowed_warning"),
	"figaro.ratelimit.7d_reset":                attribute.Int64Value(1794880800),
	"figaro.ratelimit.7d_utilization":          attribute.Float64Value(0.92),
	"figaro.ratelimit.7d_surpassed_threshold":  attribute.Float64Value(0.75),
	"figaro.ratelimit.fallback_percentage":     attribute.Float64Value(0.5),
	"figaro.ratelimit.overage_status":          attribute.StringValue("rejected"),
	"figaro.ratelimit.overage_disabled_reason": attribute.StringValue("out_of_credits"),
	"figaro.ratelimit.upgrade_paths":           attribute.StringValue("max_20x"),
}

func roundSpanAttrs(t *testing.T, respond func(w http.ResponseWriter)) (map[attribute.Key]attribute.Value, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("request-id", "req_secretish")
		w.Header().Set("anthropic-organization-id", "org-1")
		respond(w)
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}})
	require.NoError(t, err)
	rows, err := be.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)
	a, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, nilResolver{},
		func(string) (arialog.Log[[]json.RawMessage], error) { return rows, nil })
	require.NoError(t, err)
	route := provider.AnthropicDirect()
	route.BaseURL = srv.URL + "/v1"
	a.Route, a.HTTPClient = route, srv.Client()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "figaro.round")
	sendErr := a.Send(ctx, provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &recordingBus{})
	span.End()

	ended := rec.Ended()
	require.Len(t, ended, 1)
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range ended[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	return got, sendErr
}

func setUnified(h http.Header) {
	for k, v := range unifiedHeaders {
		h.Set(k, v)
	}
}

func TestTheRoundSpanCarriesTheUnifiedRateLimitOnSuccess(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	})
	require.NoError(t, err)
	require.Equal(t, unifiedAttrs, got)
}

func TestTheRoundSpanCarriesTheUnifiedRateLimitOnARejected429(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("Retry-After", "132670")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var limit *provider.RateLimited
	require.True(t, errors.As(err, &limit), "got %v", err)
	want := map[attribute.Key]attribute.Value{}
	for k, v := range unifiedAttrs {
		want[k] = v
	}
	want["figaro.ratelimit.status"] = attribute.StringValue("rejected")
	require.Equal(t, want, got)
}

func TestTheRoundSpanCarriesTheUnifiedRateLimitOnAnErrorResponse(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"nope"}}`)
	})
	require.Error(t, err)
	require.Equal(t, unifiedAttrs, got)
}

func TestAnAbsentOrUnreadableRateLimitHeaderIsAnAbsentAttribute(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		w.Header().Set("anthropic-ratelimit-unified-7d-utilization", "lots")
		w.Header().Set("anthropic-ratelimit-unified-7d-reset", "tomorrow")
		w.Header().Set("anthropic-ratelimit-unified-status", "")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "5")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	})
	require.NoError(t, err)
	for k := range got {
		require.False(t, strings.HasPrefix(string(k), "figaro.ratelimit."), "unexpected %s", k)
	}
}

// The span keeps a bounded attribute set: a response that invents unified-*
// headers must not crowd the round's own attributes out past the SDK's cap.
func TestUnknownUnifiedHeadersNeverBecomeAttributes(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		for i := 0; i < 300; i++ {
			w.Header().Set(fmt.Sprintf("anthropic-ratelimit-unified-invented-%d-utilization", i), "0.5")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	})
	require.NoError(t, err)
	require.Equal(t, unifiedAttrs, got)
}

func TestRou2UpstreamAttributesTheRateLimitToTheServingSeat(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("X-Rou2-Upstream", "anthropic-kic-2")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	})
	require.NoError(t, err)
	require.Equal(t, attribute.StringValue("anthropic-kic-2"), got["rou2.upstream"])
	require.Equal(t, attribute.StringValue("claude-test"), got["figaro.ratelimit.requested_model"])
}

func TestRou2UpstreamOnARejected429StillAttributes(t *testing.T) {
	got, _ := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("X-Rou2-Upstream", "anthropic-kic-2")
		w.Header().Set("Retry-After", "132670")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	require.Equal(t, attribute.StringValue("anthropic-kic-2"), got["rou2.upstream"])
}

func TestWithoutRou2UpstreamTheSpanIsUnchanged(t *testing.T) {
	got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
		setUnified(w.Header())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	})
	require.NoError(t, err)
	require.Equal(t, unifiedAttrs, got)
}

func TestAMalformedRou2UpstreamIsIgnored(t *testing.T) {
	for _, bad := range []string{"", "  ", "Anthropic KIC", "a/b", strings.Repeat("a", 65)} {
		got, err := roundSpanAttrs(t, func(w http.ResponseWriter) {
			setUnified(w.Header())
			w.Header().Set("X-Rou2-Upstream", bad)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
		})
		require.NoError(t, err)
		require.Equal(t, unifiedAttrs, got, "value %q", bad)
	}
}
