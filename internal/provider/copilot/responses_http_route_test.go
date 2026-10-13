package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// A catalog entry that offers only "/responses" must take the HTTP route.
const grokTextSSE = `event: response.created
data: {"response":{"id":"r1","status":"in_progress","output":[],"usage":null},"sequence_number":0,"type":"response.created"}

event: response.in_progress
data: {"response":{"id":"r1","status":"in_progress","output":[],"usage":null},"sequence_number":1,"type":"response.in_progress"}

event: response.output_item.added
data: {"item":{"id":"rs-a","status":"in_progress","summary":[],"type":"reasoning"},"output_index":0,"sequence_number":2,"type":"response.output_item.added"}

event: response.output_item.done
data: {"item":{"encrypted_content":"opaque","id":"rs-b","status":"completed","summary":[],"type":"reasoning"},"output_index":0,"sequence_number":3,"type":"response.output_item.done"}

event: response.output_item.added
data: {"item":{"content":[],"id":"msg-a","role":"assistant","status":"in_progress","type":"message"},"output_index":1,"sequence_number":4,"type":"response.output_item.added"}

event: response.content_part.added
data: {"content_index":0,"item_id":"msg-b","output_index":1,"part":{"annotations":[],"logprobs":[],"text":"","type":"output_text"},"sequence_number":5,"type":"response.content_part.added"}

event: response.output_text.delta
data: {"content_index":0,"delta":"pong","item_id":"msg-c","logprobs":[],"output_index":1,"sequence_number":6,"type":"response.output_text.delta"}

event: response.output_text.done
data: {"content_index":0,"item_id":"msg-d","logprobs":[],"output_index":1,"sequence_number":7,"text":"pong","type":"response.output_text.done"}

event: response.content_part.done
data: {"content_index":0,"item_id":"msg-e","output_index":1,"part":{"annotations":[],"logprobs":[],"text":"pong","type":"output_text"},"sequence_number":8,"type":"response.content_part.done"}

event: response.output_item.done
data: {"item":{"content":[{"annotations":[],"logprobs":[],"text":"pong","type":"output_text"}],"id":"msg-f","role":"assistant","status":"completed","type":"message"},"output_index":1,"sequence_number":9,"type":"response.output_item.done"}

event: response.completed
data: {"response":{"id":"r1","status":"completed","output":[{"encrypted_content":"opaque","id":"rs-c","status":"completed","summary":[],"type":"reasoning"},{"content":[{"annotations":[],"logprobs":[],"text":"pong","type":"output_text"}],"id":"msg-g","role":"assistant","status":"completed","type":"message"}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}},"sequence_number":10,"type":"response.completed"}

`

type copilotRouteServer struct {
	mu        sync.Mutex
	sockets   int
	posts     int
	postBody  map[string]any
	postHead  http.Header
	postPaths []string
}

func (s *copilotRouteServer) snapshot() (int, int, map[string]any, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sockets, s.posts, s.postBody, s.postHead
}

// newCopilotRouteServer counts each transport; the POST side replies with
// sse, or with the first of rejects while any remain.
func newCopilotRouteServer(t *testing.T, model string, endpoints []string, sse string, rejects ...string) (*httptest.Server, *copilotRouteServer) {
	t.Helper()
	rec := &copilotRouteServer{}
	socket := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		var req map[string]any
		if websocket.JSON.Receive(conn, &req) != nil {
			return
		}
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.output_text.delta", "delta": "socket"})
		_ = websocket.JSON.Send(conn, map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "socket"},
			}}},
		}})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
				"id": model, "name": model, "model_picker_enabled": true,
				"supported_endpoints": endpoints,
			}}})
		case r.URL.Path == "/responses" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket"):
			rec.mu.Lock()
			rec.sockets++
			rec.mu.Unlock()
			socket.ServeHTTP(w, r)
		case r.URL.Path == "/responses" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			rec.mu.Lock()
			rec.posts++
			rec.postBody, rec.postHead = body, r.Header.Clone()
			var reject string
			if len(rejects) > 0 {
				reject, rejects = rejects[0], rejects[1:]
			}
			rec.mu.Unlock()
			if reject != "" {
				var status int
				var payload string
				fmt.Sscanf(reject, "%d", &status)
				payload = strings.TrimSpace(strings.TrimPrefix(reject, fmt.Sprint(status)))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, payload)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range strings.SplitAfter(sse, "\n\n") {
				_, _ = io.WriteString(w, frame)
				w.(http.Flusher).Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func sendThroughCopilot(t *testing.T, srv *httptest.Server, model string, resolver auth.TokenResolver) (*captureBus, error) {
	t.Helper()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("reply with the single word pong")},
	}}); err != nil {
		t.Fatal(err)
	}
	responsesRows, err := be.OpenTranslator(aria, "copilot-responses")
	if err != nil {
		t.Fatal(err)
	}
	c, err := newTestCopilot(srv.URL, model, resolver,
		func(aria, ns string) (store.Log[[]json.RawMessage], error) {
			if ns == "copilot-responses" {
				return responsesRows, nil
			}
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	bus := &captureBus{}
	err = c.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, bus)
	return bus, err
}

func TestAModelOfferedOnlyOverHTTPStreamsOverHTTP(t *testing.T) {
	srv, rec := newCopilotRouteServer(t, "grok-test", []string{"/responses"}, grokTextSSE)

	bus, err := sendThroughCopilot(t, srv, "grok-test", staticResolver("gh-token"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sockets, posts, body, head := rec.snapshot()
	if sockets != 0 {
		t.Errorf("dialled %d socket(s): the catalog offers this model no socket", sockets)
	}
	if posts != 1 {
		t.Fatalf("POST /responses %d times, want 1", posts)
	}
	if got := bus.text.String(); got != "pong" {
		t.Errorf("streamed %q, want %q", got, "pong")
	}
	if !bus.sawFigaro || len(bus.caches) == 0 {
		t.Fatal("the assistant message, or its native output, never reached the bus")
	}
	if got := string(bus.caches[0].Payload[len(bus.caches[0].Payload)-1]); !strings.Contains(got, "pong") {
		t.Errorf("the native payload does not carry the reply: %s", got)
	}
	if body["stream"] != true {
		t.Errorf("stream=%v: without it the endpoint answers one JSON object, not events", body["stream"])
	}
	if body["model"] != "grok-test" {
		t.Errorf("model=%v", body["model"])
	}
	for _, envelope := range []string{"type", "agent_task_id", "headers", "initiator"} {
		if _, ok := body[envelope]; ok {
			t.Errorf("the body carries the socket envelope field %q", envelope)
		}
	}
	if got := head.Get("Authorization"); got != "Bearer gh-token" {
		t.Errorf("Authorization=%q", got)
	}
	if got := head.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept=%q, want text/event-stream", got)
	}
	if head.Get("X-Interaction-Id") == "" || head.Get("Copilot-Integration-Id") == "" {
		t.Error("the Copilot request headers did not reach the POST")
	}
}

// A model that offers the socket still gets one.
func TestAModelOfferedASocketStillGetsTheSocket(t *testing.T) {
	srv, rec := newCopilotRouteServer(t, "gpt-test", []string{"/responses", "ws:/responses"}, grokTextSSE)

	bus, err := sendThroughCopilot(t, srv, "gpt-test", staticResolver("gh-token"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sockets, posts, _, _ := rec.snapshot()
	if sockets != 1 || posts != 0 {
		t.Fatalf("sockets=%d posts=%d, want the socket once and no POST", sockets, posts)
	}
	if got := bus.text.String(); got != "socket" {
		t.Errorf("streamed %q, want the socket's reply", got)
	}
}

type countingResolver struct {
	mu          sync.Mutex
	tokens      []string
	invalidated []string
}

func (r *countingResolver) Resolve() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[len(r.invalidated)], nil
}

func (r *countingResolver) Invalidate(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, token)
	return nil
}

// A 401 on the POST is an expired session: invalidate, mint, send once more.
func TestAnHTTPUnauthorizedRemintsAndRetriesOnce(t *testing.T) {
	srv, rec := newCopilotRouteServer(t, "grok-test", []string{"/responses"}, grokTextSSE,
		`401 {"error":{"message":"token expired","code":"unauthorized"}}`)
	resolver := &countingResolver{tokens: []string{"stale", "fresh"}}

	bus, err := sendThroughCopilot(t, srv, "grok-test", resolver)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_, posts, _, head := rec.snapshot()
	if posts != 2 {
		t.Fatalf("POSTs=%d, want the rejected one and one retry", posts)
	}
	if len(resolver.invalidated) != 1 || resolver.invalidated[0] != "stale" {
		t.Errorf("invalidated %v, want exactly the stale token", resolver.invalidated)
	}
	if got := head.Get("Authorization"); got != "Bearer fresh" {
		t.Errorf("the retry presented %q", got)
	}
	if bus.text.String() != "pong" {
		t.Errorf("streamed %q after the retry", bus.text.String())
	}
}

func TestAnHTTPRejectionIsClassifiedNotRetried(t *testing.T) {
	srv, rec := newCopilotRouteServer(t, "grok-test", []string{"/responses"}, grokTextSSE,
		`400 {"error":{"message":"model is not accessible via this endpoint","code":"unsupported_api_for_model"}}`)

	_, err := sendThroughCopilot(t, srv, "grok-test", staticResolver("gh-token"))
	if err == nil {
		t.Fatal("a 400 must fail the send")
	}
	if _, posts, _, _ := rec.snapshot(); posts != 1 {
		t.Errorf("POSTs=%d, want 1", posts)
	}
	if got := err.Error(); !strings.Contains(got, "status 400") {
		t.Errorf("error %q does not carry the status", got)
	}
	if strings.Contains(err.Error(), "not accessible") {
		t.Errorf("error %q carries the server's raw text, which this provider never retains", err)
	}
}

func TestAnHTTPStreamThatEndsEarlyIsAnError(t *testing.T) {
	cut := grokTextSSE[:strings.Index(grokTextSSE, "event: response.completed")]
	srv, _ := newCopilotRouteServer(t, "grok-test", []string{"/responses"}, cut)

	bus, err := sendThroughCopilot(t, srv, "grok-test", staticResolver("gh-token"))
	if err == nil {
		t.Fatal("a stream with no terminal event reported success")
	}
	if !strings.Contains(err.Error(), "receive") {
		t.Errorf("error %q does not say the stream broke", err)
	}
	if bus.text.String() != "pong" {
		t.Errorf("the deltas that did arrive were not streamed: %q", bus.text.String())
	}
}
