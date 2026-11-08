package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

var dumpCompleted = map[string]any{
	"type": "response.completed",
	"response": map[string]any{
		"status": "completed",
		"usage":  map[string]any{"input_tokens": 1, "output_tokens": 1},
		"output": []any{map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "salve"}},
		}},
	},
}

func readDump(t *testing.T, dir, suffix string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "aria-dump", "*"+suffix))
	require.NoError(t, err)
	require.Len(t, matches, 1, "want one %s in %s", suffix, dir)
	raw, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	return string(raw)
}

func sendDumped(t *testing.T, p *Provider, dir string) {
	t.Helper()
	require.NoError(t, p.Send(context.Background(), provider.SendInput{
		AriaID: "aria-dump",
		FigLog: newResponsesInputLog(t),
		Snapshot: form.FromMap(map[string]json.RawMessage{
			"system.environment.figaro_wire_dir": json.RawMessage(fmt.Sprintf("%q", dir)),
		}),
	}, &responseTestBus{}))
}

// figaro_wire_dir is how a live script reads what a model was sent. The
// anthropic and chat providers honored it and responses did not, so every
// gpt, grok and muse turn went unrecorded and a cross-provider check of the
// request bytes could see only two wires of three. Both responses transports
// dump the request they sent and every frame they read, with no credential.
func TestResponsesDumpsItsWireOnBothTransports(t *testing.T) {
	t.Run("socket", func(t *testing.T) {
		srv := newResponseServer(t, func(conn *websocket.Conn) {
			defer conn.Close()
			var req responseCreateRequest
			require.NoError(t, websocket.JSON.Receive(conn, &req))
			require.NoError(t, websocket.JSON.Send(conn, map[string]any{"type": "response.output_text.delta", "delta": "salve"}))
			require.NoError(t, websocket.JSON.Send(conn, dumpCompleted))
		})
		dir := t.TempDir()
		sendDumped(t, newResponsesTestProvider(srv, store.NewMemLog[[]json.RawMessage]()), dir)

		req, resp := readDump(t, dir, ".req.http"), readDump(t, dir, ".resp.http")
		require.True(t, strings.HasPrefix(req, "WS /responses "), "request line: %q", strings.SplitN(req, "\n", 2)[0])
		require.Contains(t, req, `"response.create"`)
		require.Contains(t, req, `ciao`)
		require.Contains(t, req, "Authorization: Bearer [redacted]")
		require.NotContains(t, req+resp, "test-token")
		require.True(t, strings.HasPrefix(resp, "HTTP/1.1 101 "), "status line: %q", strings.SplitN(resp, "\n", 2)[0])
		require.Contains(t, resp, `"response.completed"`)
	})

	t.Run("http", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []any{map[string]any{"type": "response.output_text.delta", "delta": "salve"}, dumpCompleted} {
				raw, _ := json.Marshal(ev)
				fmt.Fprintf(w, "data: %s\n\n", raw)
			}
		}))
		t.Cleanup(srv.Close)
		p := newResponsesTestProvider(srv, store.NewMemLog[[]json.RawMessage]())
		p.SetTransport("gpt-5.6-terra", HTTP)
		dir := t.TempDir()
		sendDumped(t, p, dir)

		req, resp := readDump(t, dir, ".req.http"), readDump(t, dir, ".resp.http")
		require.True(t, strings.HasPrefix(req, "POST /responses "), "request line: %q", strings.SplitN(req, "\n", 2)[0])
		require.Contains(t, req, `ciao`)
		require.NotContains(t, req+resp, "test-token")
		require.True(t, strings.HasPrefix(resp, "HTTP/1.1 200 "), "status line: %q", strings.SplitN(resp, "\n", 2)[0])
		require.Contains(t, resp, `"response.completed"`)
	})
}
