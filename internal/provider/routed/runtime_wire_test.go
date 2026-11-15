package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRou2WireCatalogDrivesNativeAutoFallback(t *testing.T) {
	raw, err := os.ReadFile("testdata/runtime-auto.json")
	require.NoError(t, err)
	for _, alias := range []string{"sol-opus@auto", "fable-sol@auto"} {
		for _, configured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/configured=%t", alias, configured), func(t *testing.T) {
				var fixture struct {
					Catalog struct {
						Data []map[string]any `json:"data"`
					} `json:"catalog"`
				}
				require.NoError(t, json.Unmarshal(raw, &fixture))
				if configured {
					for _, entry := range fixture.Catalog.Data {
						entry["supports_tools"] = true
						entry["capabilities"] = map[string]any{"image_input": map[string]any{"supported": true}}
					}
				}
				catalog, err := json.Marshal(fixture.Catalog)
				require.NoError(t, err)
				primary, target, path := "/responses", "modelflare/claude-opus-5-5", "/messages"
				if alias == "fable-sol@auto" {
					primary, target, path = "/messages", "explabs/gpt-6.1-sol", "/responses"
				}
				var requests []hit
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						w.Header().Set("Content-Type", "application/json")
						w.Write(catalog)
						return
					}
					var body struct {
						Model string `json:"model"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					requests = append(requests, hit{path: r.URL.Path, model: body.Model, headers: r.Header.Clone()})
					if len(requests) == 1 {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(409)
						fmt.Fprintf(w, `{"error":{"code":"route_fallback","retry_safe":true,"generation":"example-generation","route":{"model":%q,"api":%q},"attempt_token":"wire-token"}}`, target, path[1:])
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if path == "/messages" {
						io.WriteString(w, messagesSSE)
					} else {
						io.WriteString(w, responsesSSE)
					}
				}))
				defer srv.Close()
				p := gatewayAt(t, "wire-auto-"+alias+fmt.Sprint(configured), "preset = \"gateway\"\nbase_url = \""+srv.URL+"\"\n")
				_, err = p.Models(context.Background())
				require.NoError(t, err)
				backend, id := store.NewTestAria(t, "wire-auto", message.Patch{})
				p.translators = func(aria, namespace string) (arialog.Log[[]json.RawMessage], error) {
					return backend.OpenTranslator(aria, namespace)
				}
				log, err := backend.OpenFigIR(id)
				require.NoError(t, err)
				_, err = log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}}})
				require.NoError(t, err)
				model, _ := json.Marshal(alias)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				err = p.Send(ctx, provider.SendInput{AriaID: id, FigLog: log, Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": model}), MaxTokens: 64, Tools: []provider.Tool{{Name: "echo"}}}, &captureBus{})
				require.NotEmpty(t, requests)
				require.Equal(t, primary, requests[0].path)
				if configured {
					require.NoError(t, err)
					require.Len(t, requests, 2)
					require.Equal(t, path, requests[1].path)
					require.Equal(t, target, requests[1].model)
					require.Equal(t, "wire-token", requests[1].headers.Get(provider.AttemptTokenHeader))
				} else {
					require.ErrorContains(t, err, "target tool support is not established")
					require.Len(t, requests, 1)
				}
			})
		}
	}
}
