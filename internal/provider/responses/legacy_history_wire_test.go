package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestExistingResponsesRowsRemainByteEqualTo0696OnTheWire(t *testing.T) {
	legacy := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"old first"},{"type":"input_text","text":"old second"}]}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old reply"}]}`),
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.GreaterOrEqual(t, len(body.Input), 3)
		require.Equal(t, legacy, body.Input[:2], "pre-existing .6 cache rows must pass through byte-for-byte")
		var fresh responseInputItem
		require.NoError(t, json.Unmarshal(body.Input[2], &fresh))
		require.Equal(t, "new first", fresh.Content[0].Text)
		require.Equal(t, "\n\nnew second", fresh.Content[1].Text)
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
	}))
	defer server.Close()
	ir := arialog.NewMemLog[message.Message]()
	cache := arialog.NewMemLog[[]json.RawMessage]()
	p := New(provider.Knobs{Model: "test"}, &staticResponseTokenSource{token: "fake"}, Config{Namespace: "legacy", BaseURL: func(string) string { return server.URL }, DisablePromptCacheBreakpoints: true}, func(string) (arialog.Log[[]json.RawMessage], error) { return cache, nil })
	p.SetTransport("test", HTTP)
	for i, role := range []message.Role{message.RoleInput, message.RoleOutput} {
		e, err := ir.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: role, Content: []message.Content{message.TextContent("old")}}})
		require.NoError(t, err)
		hash, err := arialog.FigaroHash(e.Payload)
		require.NoError(t, err)
		_, err = cache.Append(arialog.Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{legacy[i]}, Fingerprint: p.Fingerprint(), FigaroHash: hash})
		require.NoError(t, err)
	}
	_, err := ir.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("new first"), message.TextContent("new second")}}})
	require.NoError(t, err)
	in := provider.SendInput{AriaID: "legacy-cache", FigLog: ir}
	require.NoError(t, p.Send(context.Background(), in, &captureBus{}))
	require.NoError(t, p.Send(context.Background(), in, &captureBus{}))
	require.Equal(t, 2, requests)
	rows := cache.Read()
	require.Equal(t, legacy[0], rows[0].Payload[0])
	require.Equal(t, legacy[1], rows[1].Payload[0])
}
