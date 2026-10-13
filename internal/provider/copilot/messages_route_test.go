package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
	"github.com/jack-work/figaro/internal/store"
)

// The endpoint comes from the token, the Copilot headers are present, the
// dialect version is the vendor's, and the assistant message lands.
func TestTheMessagesRouteReachesTheCopilotEndpoint(t *testing.T) {
	var (
		gotPath   string
		gotHeader http.Header
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ciao"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		} {
			fmt.Fprint(w, ev+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("say ciao")},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := be.OpenTranslator(aria, "copilot-messages")
	if err != nil {
		t.Fatal(err)
	}

	c, err := newTestCopilot(srv.URL, "claude-test", staticResolver("gh-token"),
		func(aria, ns string) (store.Log[[]json.RawMessage], error) {
			if ns == "copilot-messages" {
				return rows, nil
			}
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}

	bus := &captureBus{}
	if err := c.Send(context.Background(), provider.SendInput{
		AriaID: aria, FigLog: figLog, MaxTokens: 64,
	}, bus); err != nil {
		t.Fatalf("send: %v", err)
	}

	// POST /messages is a 404; the endpoint answers /v1/messages.
	if gotPath != "/v1/messages" {
		t.Errorf("path=%q, want /v1/messages", gotPath)
	}
	if got := gotHeader.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("Authorization=%q, want a bearer token from the token source", got)
	}
	if got := gotHeader.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version=%q, want %q -- the VENDOR dialect version, not GitHub's",
			got, "2023-06-01")
	}
	if got := gotHeader.Get("X-GitHub-Api-Version"); got != copilotAPIVersion {
		t.Errorf("X-GitHub-Api-Version=%q, want %q", got, copilotAPIVersion)
	}
	if got := gotHeader.Get("Copilot-Integration-Id"); got == "" {
		t.Error("the Copilot static headers did not reach the request")
	}
	if got := gotHeader.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key=%q: this endpoint takes a bearer, and the key header must not be sent", got)
	}
	if strings.Contains(string(gotBody), "eager_input_streaming") {
		t.Error("the request carried eager_input_streaming, which this endpoint refuses")
	}
	if !bus.sawFigaro {
		t.Error("no assistant message reached the bus")
	}
}

type staticResolver string

func (s staticResolver) Resolve() (string, error) { return string(s), nil }
func (s staticResolver) Invalidate(string) error  { return nil }

type captureBus struct {
	sawFigaro bool
	lastStop  string
	caches    []provider.AssistantCache
	text      strings.Builder
	onDelta   func()
}

func (b *captureBus) PushDelta(c message.Content) {
	b.text.WriteString(c.Text)
	if b.onDelta != nil {
		b.onDelta()
	}
}
func (b *captureBus) PushToolInvokeStart(string, string) {}
func (b *captureBus) PushToolInvokeDelta(string, string) {}
func (b *captureBus) PushToolReady(message.Content)      {}
func (b *captureBus) PushMessageEnd(stop string)         { b.lastStop = stop }
func (b *captureBus) PushFigaro(_ message.Message, caches ...provider.AssistantCache) {
	b.sawFigaro = true
	b.caches = append(b.caches, caches...)
}

func newTestCopilot(base, model string, resolver auth.TokenResolver, translators routed.Translators) (*routed.Provider, error) {
	tokens := newTokenSource(resolver, Config{BaseURL: base})
	return routed.New(Spec(tokens), provider.Knobs{Model: model, MaxTokens: 64}, tokens, translators)
}
