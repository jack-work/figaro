package responses

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
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestQueuedSenderPartsHaveAnExplicitSeparatorOnTheResponsesWire(t *testing.T) {
	var flattened string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []responseInputItem `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		for _, item := range body.Input {
			for _, part := range item.Content {
				if part.Type == "input_text" {
					flattened += part.Text
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
	}))
	defer server.Close()
	log := arialog.NewMemLog[message.Message]()
	_, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.SenderText("Gluck", "first queued message"), message.SenderText("user", "second queued message")}}})
	require.NoError(t, err)
	cache := arialog.NewMemLog[[]json.RawMessage]()
	p := New(provider.Knobs{Model: "test"}, &staticResponseTokenSource{token: "fake"}, Config{Namespace: "queue-boundary", BaseURL: func(string) string { return server.URL }}, func(string) (arialog.Log[[]json.RawMessage], error) { return cache, nil })
	p.SetTransport("test", HTTP)
	require.NoError(t, p.Send(context.Background(), provider.SendInput{AriaID: "queued-parts", FigLog: log}, &captureBus{}))
	require.Equal(t, "first queued message\n\nsecond queued message", flattened)
}

func TestInputBoundaryNormalizationPreservesContentAndCachedRows(t *testing.T) {
	items, err := encodeResponseMessage(message.Message{Role: message.RoleInput, Content: []message.Content{message.SenderText(forkID, " preserve  spaces\ninside "), message.TextContent("next")}}, nil, form.Snapshot{}, nil)
	require.NoError(t, err)
	before := append([]byte(nil), items[0]...)
	changed, err := separateInputTextParts(items)
	require.NoError(t, err)
	require.Equal(t, before, []byte(items[0]), "do not change cached prefix rows")
	var item responseInputItem
	require.NoError(t, json.Unmarshal(changed[0], &item))
	require.Equal(t, "\n\n preserve  spaces\ninside ", item.Content[1].Text)
	require.Equal(t, "\n\nnext", item.Content[2].Text)
	twice, err := separateInputTextParts(changed)
	require.NoError(t, err)
	require.Equal(t, changed, twice)
	output := json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":" b"}]}`)
	untouched, err := separateInputTextParts([]json.RawMessage{output})
	require.NoError(t, err)
	require.Equal(t, output, untouched[0])
	require.True(t, strings.Contains(item.Content[0].Text, "sender"))
}

func TestInputBoundariesKeepTheHistoryPrefixByteStableAcrossTurns(t *testing.T) {
	history := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"first queued message"},{"type":"input_text","text":"second queued message"}]}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reply with spaces"}]}`),
	}
	first, err := separateInputTextParts(history)
	require.NoError(t, err)
	next := append(append([]json.RawMessage(nil), history...), json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"next inquiry"},{"type":"input_text","text":"next reminder"}]}`))
	second, err := separateInputTextParts(next)
	require.NoError(t, err)
	require.Equal(t, first, second[:len(first)], "adding a turn must not change the normalized history prefix")
	repeated, err := separateInputTextParts(next)
	require.NoError(t, err)
	require.Equal(t, second, repeated)
}
