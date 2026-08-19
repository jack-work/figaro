package anthropicsdk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

type nopBus struct{}

func (nopBus) PushDelta(message.Content)                              {}
func (nopBus) PushFigaro(message.Message, ...provider.AssistantCache) {}
func (nopBus) PushToolInvokeStart(string, string)                     {}
func (nopBus) PushToolInvokeDelta(string, string)                     {}
func (nopBus) PushToolReady(message.Content)                          {}
func (nopBus) PushMessageEnd(string)                                  {}

var sdkRequiredFields = map[string][]string{
	"text":              {"text"},
	"thinking":          {"thinking", "signature"},
	"redacted_thinking": {"data"},
	"tool_use":          {"id", "name", "input"},
}

func assertAPILegalSDKPayload(t *testing.T, payload []json.RawMessage) {
	t.Helper()
	for _, raw := range payload {
		var msg struct {
			Content []map[string]interface{} `json:"content"`
		}
		require.NoError(t, json.Unmarshal(raw, &msg))
		require.NotEmpty(t, msg.Content)
		for _, block := range msg.Content {
			typ, _ := block["type"].(string)
			require.NotEmpty(t, typ)
			for _, field := range sdkRequiredFields[typ] {
				require.Contains(t, block, field, "block %v missing %q", block, field)
			}
			if s, ok := block["text"].(string); ok {
				assert.NotEmpty(t, strings.TrimSpace(s))
			}
			if s, ok := block["thinking"].(string); ok {
				sig, _ := block["signature"].(string)
				assert.True(t, strings.TrimSpace(s) != "" || sig != "", "thinking block needs summary or signature: %v", block)
			}
		}
	}
}

func TestAssistantCacheDropsEmptyStreamedBlocks(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-only"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"salve"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":2}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	decoder := ssestream.NewDecoder(&http.Response{Body: io.NopCloser(strings.NewReader(sse))})
	stream := ssestream.NewStream[anthropic.MessageStreamEventUnion](decoder, nil)
	msg, acc, err := drainStream(context.Background(), stream, "claude-test", nopBus{})
	require.NoError(t, err)
	require.Len(t, acc.Content, 3)
	require.Len(t, msg.Content, 1)
	assert.Equal(t, "salve", msg.Content[0].Text)

	p := &Provider{reminder: "tag", CacheNamespace: "anthropic"}
	native, err := p.assistantCache(acc)
	require.NoError(t, err)
	require.Len(t, native.Payload, 1)
	assertAPILegalSDKPayload(t, native.Payload)

	raw := string(native.Payload[0])
	assert.Contains(t, raw, `"text":"salve"`)
	assert.Contains(t, raw, `"sig-only"`)
	assert.Contains(t, raw, `"thinking":""`)
}

func TestAssistantCacheEmptiesToNoPayload(t *testing.T) {
	var acc anthropic.Message
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
		"content":[{"type":"text","text":""},{"type":"thinking","thinking":"","signature":""}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`), &acc))
	p := &Provider{reminder: "tag", CacheNamespace: "anthropic"}
	native, err := p.assistantCache(acc)
	require.NoError(t, err)
	assert.Empty(t, native.Payload)
}

func TestAssistantCacheKeepsSignedEmptyThinking(t *testing.T) {
	var acc anthropic.Message
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
		"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"t1","name":"bash","input":{"command":"ls"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
	}`), &acc))
	p := &Provider{reminder: "tag", CacheNamespace: "anthropic"}
	native, err := p.assistantCache(acc)
	require.NoError(t, err)
	require.Len(t, native.Payload, 1)
	raw := string(native.Payload[0])
	assert.Contains(t, raw, `"signature":"sig"`)
	assert.Contains(t, raw, `"thinking":""`)
	assert.Contains(t, raw, `"id":"t1"`)
}

func TestAssistantCacheInvalidToolInputUncacheable(t *testing.T) {
	var acc anthropic.Message
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
		"content":[{"type":"text","text":"salve"},{"type":"tool_use","id":"t1","name":"bash","input":{}}],
		"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
	}`), &acc))
	acc.Content[1].Input = []byte(`"truncated JSON string`)
	p := &Provider{reminder: "tag", CacheNamespace: "anthropic"}
	native, err := p.assistantCache(acc)
	require.NoError(t, err)
	assert.Empty(t, native.Payload)
}

func TestValidAccumulatedBlockMatchesDecoderSkip(t *testing.T) {
	cases := []string{
		`{"type":"text","text":""}`,
		`{"type":"text","text":"   "}`,
		`{"type":"text","text":"salve"}`,
		`{"type":"thinking","thinking":"","signature":"sig-only"}`,
		`{"type":"thinking","thinking":"hm","signature":"sig"}`,
		`{"type":"tool_use","id":"t1","name":"bash","input":{}}`,
	}
	for _, c := range cases {
		var m anthropic.Message
		require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[`+c+`],"usage":{"input_tokens":0,"output_tokens":0}}`), &m))
		decoded := decodeAssistantMessage(m)
		if validAccumulatedBlock(m.Content[0]) {
			assert.Len(t, decoded.Content, 1, "block %s", c)
		} else {
			assert.Empty(t, decoded.Content, "block %s", c)
		}
	}
}

func TestCacheableSDKBlockShapesCarryRequiredFields(t *testing.T) {
	cases := []string{
		`{"type":"text","text":"salve"}`,
		`{"type":"thinking","thinking":"hm","signature":"sig"}`,
		`{"type":"redacted_thinking","data":"opaque"}`,
		`{"type":"tool_use","id":"t1","name":"bash","input":{"cmd":"ls"}}`,
	}
	for _, c := range cases {
		var b anthropic.ContentBlockUnion
		require.NoError(t, json.Unmarshal([]byte(c), &b))
		require.True(t, validAccumulatedBlock(b), "block %s", c)
		raw, err := json.Marshal(b.ToParam())
		require.NoError(t, err)
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &m))
		require.Contains(t, sdkRequiredFields, b.Type)
		for _, field := range sdkRequiredFields[b.Type] {
			assert.Contains(t, m, field, "block %s marshaled without %q", c, field)
		}
	}
}
