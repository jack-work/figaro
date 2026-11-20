package responses

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	coder "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAWebSocketHandshakeRecordsTheServingSeatOnTheRoundSpan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rou2-Upstream", "openai-seat-1")
		conn, err := coder.Accept(w, r, &coder.AcceptOptions{InsecureSkipVerify: true})
		if err == nil {
			conn.CloseNow()
		}
	}))
	defer server.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "figaro.round")
	conn, err := dialResponses(ctx, responsesEndpoint(server.URL), http.Header{})
	require.NoError(t, err)
	conn.CloseNow()
	span.End()

	ended := rec.Ended()
	require.Len(t, ended, 1)
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range ended[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	require.Equal(t, attribute.StringValue("openai-seat-1"), got["rou2.upstream"])
}
