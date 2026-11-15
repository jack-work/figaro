package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestDeclaredResponsesOmissionReachesHTTPWithoutCatalog(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprint(omit), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/backend/responses", r.URL.Path)
				require.Equal(t, "account-example", r.Header.Get("X-Account"))
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				_, present := body["max_output_tokens"]
				require.Equal(t, !omit, present)
				require.JSONEq(t, "false", string(body["store"]))
				require.JSONEq(t, "true", string(body["stream"]))
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}))
			defer server.Close()
			config := "preset=\"gateway\"\nbase_url=\"" + server.URL + "/backend\"\n[headers]\nX-Account=\"account-example\"\n[routing]\ncatalog=\"off\"\ndefault=\"responses\"\n"
			if omit {
				config += "[responses]\nomit_fields=[\"max_output_tokens\"]\n"
			}
			p := gatewayAt(t, "subscription-wire-"+fmt.Sprint(omit), config)
			backend, id := store.NewTestAria(t, "subscription", message.Patch{})
			p.translators = func(id, ns string) (arialog.Log[[]json.RawMessage], error) { return backend.OpenTranslator(id, ns) }
			log, err := backend.OpenFigIR(id)
			require.NoError(t, err)
			_, err = log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}}})
			require.NoError(t, err)
			require.NoError(t, p.Send(context.Background(), provider.SendInput{AriaID: id, FigLog: log, Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"model"`)}), MaxTokens: 321}, &captureBus{}))
			require.Equal(t, 1, calls)
		})
	}
}
