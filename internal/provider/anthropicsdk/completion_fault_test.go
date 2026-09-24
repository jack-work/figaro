package anthropicsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// jack-work/figaro#24, on the SDK path. The hand-rolled provider and this one
// drain differently and land the same way, so the same filtered stream must
// fail the send here too, or the fix is one provider's and the other still
// records a blank reply.

type landingBus struct {
	msgs []message.Message
}

func (b *landingBus) PushDelta(message.Content)          {}
func (b *landingBus) PushToolInvokeStart(string, string) {}
func (b *landingBus) PushToolInvokeDelta(string, string) {}
func (b *landingBus) PushToolReady(message.Content)      {}
func (b *landingBus) PushMessageEnd(string)              {}
func (b *landingBus) PushFigaro(m message.Message, _ ...provider.AssistantCache) {
	b.msgs = append(b.msgs, m)
}

type tokenOf string

func (t tokenOf) Resolve() (string, error) { return string(t), nil }
func (tokenOf) Invalidate(string) error    { return nil }

func serveSSE(t *testing.T, events []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			b, err := json.Marshal(ev)
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
			w.(http.Flusher).Flush()
		}
	}))
}

func sendThrough(t *testing.T, srv *httptest.Server) (*landingBus, error) {
	t.Helper()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("say ciao")},
	}})
	require.NoError(t, err)
	rows, err := be.OpenTranslator(aria, "anthropicsdk")
	require.NoError(t, err)

	p, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, tokenOf("sk-ant-test"),
		func(string) (store.Log[[]json.RawMessage], error) { return rows, nil })
	require.NoError(t, err)
	p.ExtraOptions = []option.RequestOption{option.WithBaseURL(srv.URL), option.WithMaxRetries(0)}

	bus := &landingBus{}
	err = p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, bus)
	return bus, err
}

var (
	sdkStart = map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
		"content": []any{}, "stop_reason": nil,
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 0}}}
	sdkThinking = []map[string]any{
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "hm"}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "sig"}},
		{"type": "content_block_stop", "index": 0},
	}
	sdkStop = map[string]any{"type": "message_stop"}
)

func sdkDelta(stop string) map[string]any {
	return map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 1}}
}

func TestSDKFilteredCompletionEndsTheSendInError(t *testing.T) {
	cases := []struct {
		name     string
		events   []map[string]any
		wireWord string
		kept     bool
	}{
		{"refusal, thinking only", append(append([]map[string]any{sdkStart}, sdkThinking...), sdkDelta("refusal"), sdkStop), "refusal", true},
		{"end_turn, nothing", []map[string]any{sdkStart, sdkDelta("end_turn"), sdkStop}, "end_turn", false},
		{"refusal, nothing", []map[string]any{sdkStart, sdkDelta("refusal"), sdkStop}, "refusal", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveSSE(t, tc.events)
			defer srv.Close()
			bus, err := sendThrough(t, srv)
			require.Error(t, err, "the send returned nil: the turn will record a normal stop")
			assert.Contains(t, err.Error(), tc.wireWord)
			if !tc.kept {
				assert.Empty(t, bus.msgs)
				return
			}
			require.Len(t, bus.msgs, 1)
			assert.Equal(t, message.StopError, bus.msgs[0].StopReason)
		})
	}
}
