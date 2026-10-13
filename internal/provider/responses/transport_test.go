package responses

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

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

// A cancelled stream hands over what it accumulated, native items included.
func TestACancelledResponsesStreamHandsOverItsPartial(t *testing.T) {
	srv := newResponseServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var req responseCreateRequest
		if err := websocket.JSON.Receive(conn, &req); err != nil {
			return
		}
		_ = websocket.JSON.Send(conn, map[string]any{
			"type": "response.output_text.delta", "delta": "half a thou",
		})
		var next any
		_ = websocket.JSON.Receive(conn, &next) // cancellation closes the socket
	})
	defer srv.Close()

	p := newResponsesTestProvider(srv, store.NewMemLog[[]json.RawMessage]())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := &captureBus{onDelta: cancel} // cancel only once the consumer has the delta

	err := p.Send(ctx, provider.SendInput{
		AriaID: "aria-cancel",
		FigLog: newResponsesInputLog(t),
	}, bus)
	if err == nil {
		t.Fatal("a cancelled send must still report the cancellation")
	}
	if !bus.sawFigaro {
		t.Fatal("the partial never reached the bus: an interrupted turn loses what the model produced")
	}
	if len(bus.caches) == 0 {
		t.Fatal("the partial carries no native payload, so its translation would be a re-encode")
	}
	if got := string(bus.caches[0].Payload[0]); !strings.Contains(got, "half a thou") {
		t.Fatalf("the native payload does not carry what streamed: %s", got)
	}
	if bus.lastStop != string(message.StopAborted) {
		t.Fatalf("stop reason %q, want %q", bus.lastStop, message.StopAborted)
	}
}

func TestServerSentEventFraming(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"event and data lines", "event: a\ndata: {\"type\":\"a\"}\n\n", []string{`{"type":"a"}`}},
		{"no space after the colon", "data:{\"x\":1}\n\n", []string{`{"x":1}`}},
		{"crlf", "event: a\r\ndata: {\"x\":1}\r\n\r\n", []string{`{"x":1}`}},
		{"multi-line data joins with newline", "data: {\"x\":\ndata: 1}\n\n", []string{"{\"x\":\n1}"}},
		{"comments and ids are skipped", ": keepalive\nid: 7\ndata: {}\n\n", []string{`{}`}},
		{"a final event with no blank line", "data: {\"last\":true}", []string{`{"last":true}`}},
		{"the done sentinel ends the stream", "data: {}\n\ndata: [DONE]\n\ndata: {\"after\":1}\n\n", []string{`{}`}},
		{"two events", "data: 1\n\ndata: 2\n\n", []string{"1", "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := sseFrames(strings.NewReader(tc.in))
			var got []string
			for {
				frame, err := next(context.Background())
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("frame %d: %v", len(got), err)
				}
				got = append(got, string(frame))
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
