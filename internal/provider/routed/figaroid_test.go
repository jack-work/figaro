package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Defends #89: each transport names the figaro only when the file opts in,
// and the file's static headers cannot speak for it.
func TestEachTransportSendsFigaroIdOnlyWhenTheFileOptsIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		t.Run(fmt.Sprint("send_figaro_id=", optIn), func(t *testing.T) {
			var mu sync.Mutex
			got := map[string][]string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				mu.Lock()
				got[r.URL.Path] = r.Header.Values(provider.FigaroIDHeader)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				switch r.URL.Path {
				case "/v1/chat/completions":
					_, _ = io.WriteString(w, chatSSE)
				case "/v1/responses":
					_, _ = io.WriteString(w, responsesSSE)
				case "/v1/messages":
					_, _ = io.WriteString(w, messagesSSE)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			doc := fmt.Sprintf(`
base_url = "%s/v1"
send_figaro_id = %t
headers = { "Figaro-Id" = "forged" }
[routing]
default = "chat"
[[routing.rule]]
model = "grok-*"
transport = "responses"
[[routing.rule]]
model = "claude-*"
transport = "messages"
`, srv.URL, optIn)
			var f File
			if _, err := toml.Decode(doc, &f); err != nil {
				t.Fatal(err)
			}
			spec, err := f.Apply(Spec{Name: "gateway", AuthOptional: true})
			if err != nil {
				t.Fatal(err)
			}

			arias := map[string]string{}
			for model, path := range map[string]string{"llama-4": "/v1/chat/completions", "grok-4.7": "/v1/responses", "claude-sonnet-5.5": "/v1/messages"} {
				be, aria := store.NewTestAria(t, "d", message.Patch{})
				figLog, err := be.OpenFigIR(aria)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
					Role: message.RoleInput, Content: []message.Content{message.TextContent("say something")},
				}}); err != nil {
					t.Fatal(err)
				}
				p, err := New(spec, provider.Knobs{Model: model, MaxTokens: 64}, staticToken("k"),
					func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{}); err != nil {
					t.Fatalf("%s: %v", model, err)
				}
				arias[path] = aria
			}

			mu.Lock()
			defer mu.Unlock()
			if len(got) != 3 {
				t.Fatalf("%d paths hit, want 3: %v", len(got), got)
			}
			for path, aria := range arias {
				want := []string{"forged"}
				if optIn {
					want = []string{aria}
				}
				if fmt.Sprint(got[path]) != fmt.Sprint(want) {
					t.Errorf("%s: Figaro-Id %q, want %q", path, got[path], want)
				}
			}
		})
	}
}
