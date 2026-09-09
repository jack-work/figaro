package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestResponsesOnlyRefreshesCredentialsOnHandshake401(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var attempts atomic.Int32
			ws := websocket.Handler(func(conn *websocket.Conn) {
				defer conn.Close()
				var request responseCreateRequest
				if websocket.JSON.Receive(conn, &request) != nil {
					return
				}
				_ = websocket.JSON.Send(conn, map[string]any{"type": "response.completed", "response": map[string]any{
					"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
				}})
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if n := attempts.Add(1); n == 1 || status != 401 {
					http.Error(w, "PRIVATE_REJECTION_BODY", status)
					return
				}
				require.Equal(t, "https://github.com", r.Header.Get("Origin"))
				ws.ServeHTTP(w, r)
			}))
			defer server.Close()
			p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
			source := p.tokenSrc.(*staticResponseTokenSource)
			err := p.Send(context.Background(), provider.SendInput{AriaID: "handshake-probe", FigLog: newResponsesInputLog(t)}, &responseTestBus{})
			if status == 401 {
				require.NoError(t, err)
				require.Equal(t, int32(2), attempts.Load())
				require.Equal(t, 1, source.invalidated)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), strconv.Itoa(status))
				require.NotContains(t, err.Error(), "PRIVATE_REJECTION_BODY")
				require.Equal(t, int32(1), attempts.Load())
				require.Zero(t, source.invalidated)
			}
		})
	}
}

func TestResponsesHandshakeDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := dialResponses(context.Background(), responsesEndpoint(source.URL), http.Header{"Authorization": []string{"private-test-bearer"}})
	require.Error(t, err)
	require.Zero(t, redirected.Load())
}

func TestResponsesAcceptsCodingOutputBeyondDefaultWebSocketLimit(t *testing.T) {
	text := strings.Repeat("source line\n", 16*1024)
	server := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var req responseCreateRequest
		if websocket.JSON.Receive(conn, &req) != nil {
			return
		}
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}},
		}}}})
	})
	p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
	bus := &responseTestBus{}
	require.NoError(t, p.Send(context.Background(), provider.SendInput{AriaID: "large-frame", FigLog: newResponsesInputLog(t)}, bus))
	require.Len(t, bus.messages, 1)
	require.Equal(t, text, bus.messages[0].Content[0].Text)
}
