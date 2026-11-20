package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func TestARoutedMessagesRoundCarriesTheSeatAndRateLimitsOnItsRoundSpan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Rou2-Upstream", "anthropic-kic-2")
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.25")
		w.Header().Set("anthropic-ratelimit-unified-5h-status", "allowed")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, messagesReply)
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}})
	require.NoError(t, err)
	p := gatewayAt(t, "rou2-messages-span", "preset=\"gateway\"\nbase_url=\""+srv.URL+"\"\n[routing]\ncatalog=\"off\"\ndefault=\"messages\"\n")
	p.translators = func(id, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(id, ns) }

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "figaro.round")
	require.NoError(t, p.Send(ctx, provider.SendInput{
		AriaID: aria, FigLog: figLog, MaxTokens: 64,
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}),
	}, &captureBus{}))
	span.End()

	ended := rec.Ended()
	require.Len(t, ended, 1)
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range ended[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	require.Equal(t, attribute.StringValue("anthropic-kic-2"), got["rou2.upstream"])
	require.Equal(t, attribute.Float64Value(0.25), got["figaro.ratelimit.5h_utilization"])
	require.Equal(t, attribute.StringValue("allowed"), got["figaro.ratelimit.5h_status"])

	var eventSeat attribute.Value
	for _, ev := range ended[0].Events() {
		if ev.Name != "http.request" {
			continue
		}
		for _, kv := range ev.Attributes {
			if kv.Key == "rou2.upstream" {
				eventSeat = kv.Value
			}
		}
	}
	require.Equal(t, attribute.StringValue("anthropic-kic-2"), eventSeat, "the http.request event carries the seat too")
}
