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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Measured on Copilot's /v1/messages for claude-opus-5.5, 2026-10-18: a
// top-level effort change re-wrote every message after the first thinking
// block, a per-message change read the whole prefix. The beta header passes
// through; without it the system row is a 400.
func TestCopilotOpusCarriesEffortPerMessage(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5.5","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}`,
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

	effort := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":           json.RawMessage(`"claude-opus-5.5"`),
		"system.thinking_effort": json.RawMessage(`"low"`),
	}, nil)
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Patches: []message.Patch{effort},
		Content: []message.Content{message.TextContent("say ciao")},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := be.OpenTranslator(aria, "copilot-messages")
	if err != nil {
		t.Fatal(err)
	}
	c, err := newTestCopilot(srv.URL, "claude-opus-5.5", staticResolver("gh-token"),
		func(aria, ns string) (store.Log[[]json.RawMessage], error) {
			if ns == "copilot-messages" {
				return rows, nil
			}
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	bound := form.Snapshot{}.Apply(effort)
	if err := c.Send(context.Background(), provider.SendInput{
		AriaID: aria, FigLog: figLog, Snapshot: bound, MaxTokens: 64,
	}, &captureBus{}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := gotHeader.Get("anthropic-beta"); !strings.Contains(got, "mid-conversation-output-config-2026-07-01") {
		t.Fatalf("anthropic-beta = %q", got)
	}
	var body struct {
		OutputConfig json.RawMessage   `json:"output_config"`
		Messages     []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.OutputConfig != nil {
		t.Fatalf("top-level output_config = %s", body.OutputConfig)
	}
	if len(body.Messages) != 2 || !strings.Contains(string(body.Messages[0]), `"effort":"low"`) {
		t.Fatalf("messages = %s", body.Messages)
	}
	if !c.KeepsCacheOnChange("system.thinking_effort", bound) {
		t.Fatal("the route reports that an effort change breaks the cache")
	}
}
