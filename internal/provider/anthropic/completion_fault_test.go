package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// jack-work/figaro#24. A proxy that filters a completion answers 200 with a
// healthy-looking stream: no content, or a thinking block and then a
// stop_reason figaro had no case for. Every one of those landed in the IR as
// a normal reply and the aria went quiet with nothing in the log to say why.
// The turn must END IN ERROR, with the wire's own word for it in the text.

type recordingBus struct {
	msgs  []message.Message
	stops []string
}

func (b *recordingBus) PushDelta(message.Content)          {}
func (b *recordingBus) PushToolInvokeStart(string, string) {}
func (b *recordingBus) PushToolInvokeDelta(string, string) {}
func (b *recordingBus) PushToolReady(message.Content)      {}
func (b *recordingBus) PushMessageEnd(s string)            { b.stops = append(b.stops, s) }
func (b *recordingBus) PushFigaro(m message.Message, _ ...provider.AssistantCache) {
	b.msgs = append(b.msgs, m)
}

type nilResolver struct{}

func (nilResolver) Resolve() (string, error) { return "tok", nil }
func (nilResolver) Invalidate(string) error  { return nil }

func sseBody(events ...string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev)
		b.WriteString("\n\n")
	}
	return b.String()
}

const (
	evStart    = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}`
	evThinking = "event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}` +
		"\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}` +
		"\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}` +
		"\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}`
	evText = "event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` +
		"\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ciao"}}` +
		"\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}`
	evStop = "event: message_stop\ndata: " + `{"type":"message_stop"}`
)

func evDelta(stop string) string {
	return "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":1}}`
}

func sendSSE(t *testing.T, body string) (*recordingBus, error) {
	t.Helper()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("say ciao")},
	}})
	require.NoError(t, err)
	rows, err := be.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	a, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, nilResolver{},
		func(string) (store.Log[[]json.RawMessage], error) { return rows, nil })
	require.NoError(t, err)

	bus := &recordingBus{}
	err = a.SendWithTransport(context.Background(), provider.SendInput{
		AriaID: aria, FigLog: figLog, MaxTokens: 64,
	}, bus, false, func(context.Context, *provider.RequestBody) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	return bus, err
}

func TestAFilteredCompletionEndsTheSendInError(t *testing.T) {
	cases := []struct {
		name string
		body string
		// What the wire said, which must appear in the error so the user
		// learns the proxy's word for what happened.
		wireWord string
		// Whether a message still reaches the IR: content the model paid
		// for is kept, marked as the error it is.
		kept bool
	}{
		{"refusal, thinking only", sseBody(evStart, evThinking, evDelta("refusal"), evStop), "refusal", true},
		{"content_filter, thinking only", sseBody(evStart, evThinking, evDelta("content_filter"), evStop), "content_filter", true},
		{"refusal, nothing", sseBody(evStart, evDelta("refusal"), evStop), "refusal", false},
		{"end_turn, nothing", sseBody(evStart, evDelta("end_turn"), evStop), "end_turn", false},
		{"message_stop with no stop reason", sseBody(evStart, evText, evStop), "no stop reason", true},
		{"a stop reason from the future", sseBody(evStart, evText, evDelta("pause_turn"), evStop), "pause_turn", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus, err := sendSSE(t, tc.body)
			require.Error(t, err, "the send returned nil: the turn will record a normal stop")
			assert.Contains(t, err.Error(), tc.wireWord)
			if !tc.kept {
				assert.Empty(t, bus.msgs, "an empty completion is not a message")
				return
			}
			require.Len(t, bus.msgs, 1, "the content the model produced is kept")
			assert.Equal(t, message.StopError, bus.msgs[0].StopReason)
			assert.Equal(t, []string{string(message.StopError)}, bus.stops)
		})
	}
}

// The other side of the same instrument: a healthy stream still lands as a
// healthy message, or the guard is a tautology.
func TestAHealthyCompletionStillLands(t *testing.T) {
	for _, stop := range []string{"end_turn", "stop_sequence"} {
		t.Run(stop, func(t *testing.T) {
			bus, err := sendSSE(t, sseBody(evStart, evThinking, evText, evDelta(stop), evStop))
			require.NoError(t, err)
			require.Len(t, bus.msgs, 1)
			assert.Equal(t, message.StopEnd, bus.msgs[0].StopReason)
		})
	}
}

// An SSE error event already failed the send. It must keep doing so and
// carry the event's own text, since the stop reason it sets is "error" and
// the message would otherwise be decoded as if it had finished.
func TestAnSSEErrorEventStillFailsTheSend(t *testing.T) {
	bus, err := sendSSE(t, sseBody(evStart, evText,
		"event: error\ndata: "+`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overloaded_error")
	assert.Empty(t, bus.msgs)
}
