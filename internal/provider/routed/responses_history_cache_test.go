package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestDeclaredResponsesHistoryDoesNotInventCacheExtensions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if strings.Contains(string(raw), "prompt_cache_breakpoint") {
			http.Error(w, `{"error":{"message":"unsupported cache extension","type":"invalid_request_error"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesSSE)
	}))
	defer server.Close()
	p := gatewayAt(t, "strict-history", "preset=\"gateway\"\nbase_url=\""+server.URL+"\"\n[routing]\ncatalog=\"off\"\ndefault=\"responses\"\n")
	backend, id := store.NewTestAria(t, "strict-history", message.Patch{})
	p.translators = func(id, ns string) (store.Log[[]json.RawMessage], error) { return backend.OpenTranslator(id, ns) }
	log, err := backend.OpenFigIR(id)
	require.NoError(t, err)
	appendMessage := func(role message.Role, text string) {
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: role, Content: []message.Content{message.TextContent(text)}}})
		require.NoError(t, err)
	}
	appendMessage(message.RoleInput, "first")
	in := provider.SendInput{AriaID: id, FigLog: log, Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"model"`)}), MaxTokens: 64}
	require.NoError(t, p.Send(context.Background(), in, &captureBus{}))
	appendMessage(message.RoleOutput, "ok")
	appendMessage(message.RoleInput, "second")
	require.NoError(t, p.Send(context.Background(), in, &captureBus{}))
	require.Equal(t, 2, requests)
}
