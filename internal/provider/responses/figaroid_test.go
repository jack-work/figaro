package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Defends #89: the socket handshake carries Figaro-Id when the caller named
// one, and nothing otherwise.
func TestTheSocketHandshakeCarriesFigaroIdOnlyWhenNamed(t *testing.T) {
	for name, named := range map[string]bool{"unnamed": false, "named": true} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
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
				mu.Lock()
				got = r.Header.Values(provider.FigaroIDHeader)
				mu.Unlock()
				ws.ServeHTTP(w, r)
			}))
			defer server.Close()
			p := newResponsesTestProvider(server, store.NewMemLog[[]json.RawMessage]())
			ctx := context.Background()
			if named {
				ctx = provider.WithFigaroID(ctx, "handshake-probe")
			}
			require.NoError(t, p.Send(ctx, provider.SendInput{AriaID: "handshake-probe", FigLog: newResponsesInputLog(t)}, &responseTestBus{}))
			mu.Lock()
			defer mu.Unlock()
			if named {
				require.Equal(t, []string{"handshake-probe"}, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}
