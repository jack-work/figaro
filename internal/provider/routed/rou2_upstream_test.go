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

const chatReply = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"

const responsesReply = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

func rou2RoundAttrs(t *testing.T, transport, reply string, header map[string]string, status int) map[attribute.Key]attribute.Value {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}})
	require.NoError(t, err)
	p := gatewayAt(t, "rou2-upstream-"+transport, "preset=\"gateway\"\nbase_url=\""+srv.URL+"\"\n[routing]\ncatalog=\"off\"\ndefault=\""+transport+"\"\n")
	p.translators = func(id, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(id, ns) }

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "figaro.round")
	_ = p.Send(ctx, provider.SendInput{
		AriaID: aria, FigLog: figLog, MaxTokens: 64,
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}),
	}, &captureBus{})
	span.End()

	ended := rec.Ended()
	require.Len(t, ended, 1)
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range ended[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	return got
}

func TestARou2RoundRecordsTheServingSeatOnItsSpan(t *testing.T) {
	for _, tc := range []struct{ transport, reply string }{{"chat", chatReply}, {"responses", responsesReply}} {
		t.Run(tc.transport, func(t *testing.T) {
			got := rou2RoundAttrs(t, tc.transport, tc.reply, map[string]string{"X-Rou2-Upstream": "anthropic-kic-2"}, http.StatusOK)
			require.Equal(t, attribute.StringValue("anthropic-kic-2"), got["rou2.upstream"])
		})
	}
}

func TestARou2RoundRefusedWithAStatusStillRecordsTheServingSeat(t *testing.T) {
	for _, tc := range []struct{ transport, reply string }{{"chat", "{}"}, {"responses", "{}"}} {
		t.Run(tc.transport, func(t *testing.T) {
			got := rou2RoundAttrs(t, tc.transport, tc.reply, map[string]string{"X-Rou2-Upstream": "anthropic-kic-2"}, http.StatusBadGateway)
			require.Equal(t, attribute.StringValue("anthropic-kic-2"), got["rou2.upstream"])
		})
	}
}

func TestARou2RoundIgnoresAMalformedOrMissingServingSeat(t *testing.T) {
	for _, header := range []map[string]string{nil, {"X-Rou2-Upstream": "a/b"}, {"X-Rou2-Upstream": ""}} {
		for _, tc := range []struct{ transport, reply string }{{"chat", chatReply}, {"responses", responsesReply}} {
			got := rou2RoundAttrs(t, tc.transport, tc.reply, header, http.StatusOK)
			require.NotContains(t, got, attribute.Key("rou2.upstream"))
		}
	}
}
