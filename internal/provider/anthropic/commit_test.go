package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

func TestAssistantCacheReencodesInputReadyMessage(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	native, err := a.assistantCache(message.Message{
		Role:       message.RoleAssistant,
		Content:    []message.Content{message.TextContent("salve")},
		StopReason: message.StopEnd,
		Usage:      &message.Usage{InputTokens: 10, OutputTokens: 2},
	})
	require.NoError(t, err)
	require.Len(t, native.Payload, 1)
	raw := string(native.Payload[0])
	assert.Contains(t, raw, `"role":"assistant"`)
	assert.Contains(t, raw, `"text":"salve"`)
	assert.NotContains(t, raw, "stop_reason")
	assert.NotContains(t, raw, "usage")
	assert.Equal(t, "anthropic", native.Namespace)
	assert.Equal(t, a.Fingerprint(), native.Fingerprint)
}

type noOpBus struct{}

func (noOpBus) PushDelta(message.Content)                              {}
func (noOpBus) PushFigaro(message.Message, ...provider.AssistantCache) {}
func (noOpBus) PushToolInvokeStart(string, string)                     {}
func (noOpBus) PushToolInvokeDelta(string, string)                     {}
func (noOpBus) PushToolReady(message.Content)                          {}
func (noOpBus) PushMessageEnd(string)                                  {}

func TestNativeAssistantCachePreservesSignedAndRedactedThinking(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	nm := nativeMessage{Role: "assistant", Model: "claude-test", StopReason: "tool_use", Usage: &nativeUsage{OutputTokens: 7}}
	bus := noOpBus{}
	a.foldSSEEvent(context.Background(), "content_block_start", []byte(`{"index":0,"content_block":{"type":"thinking"}}`), &nm, nm.Usage, &nm.StopReason, bus)
	a.foldSSEEvent(context.Background(), "content_block_delta", []byte(`{"index":0,"delta":{"type":"thinking_delta","thinking":"secret"}}`), &nm, nm.Usage, &nm.StopReason, bus)
	a.foldSSEEvent(context.Background(), "content_block_delta", []byte(`{"index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`), &nm, nm.Usage, &nm.StopReason, bus)
	a.foldSSEEvent(context.Background(), "content_block_start", []byte(`{"index":1,"content_block":{"type":"redacted_thinking","data":"opaque-redacted"}}`), &nm, nm.Usage, &nm.StopReason, bus)

	cache, err := a.assistantCacheNative(nm)
	require.NoError(t, err)
	require.Len(t, cache.Payload, 1)
	var got nativeMessage
	require.NoError(t, json.Unmarshal(cache.Payload[0], &got))
	require.Len(t, got.Content, 2)
	assert.Equal(t, "thinking", got.Content[0].Type)
	assert.Equal(t, "secret", got.Content[0].Thinking)
	assert.Equal(t, "sig-abc", got.Content[0].Signature)
	assert.Equal(t, "redacted_thinking", got.Content[1].Type)
	assert.Equal(t, "opaque-redacted", got.Content[1].Data)
	assert.Empty(t, got.StopReason)
	assert.Empty(t, got.Model)
	assert.Nil(t, got.Usage)
	assert.Equal(t, "anthropic", cache.Namespace)
	assert.Equal(t, a.Fingerprint(), cache.Fingerprint)
}

var nativeRequiredFields = map[string][]string{
	"text":              {"text"},
	"thinking":          {"thinking"},
	"redacted_thinking": {"data"},
	"tool_use":          {"id", "name", "input"},
}

func assertAPILegalNativePayload(t *testing.T, payload []json.RawMessage) {
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
			for _, field := range nativeRequiredFields[typ] {
				require.Contains(t, block, field, "block %v missing %q", block, field)
			}
			if s, ok := block["text"].(string); ok {
				assert.NotEmpty(t, s)
			}
			if s, ok := block["thinking"].(string); ok {
				sig, _ := block["signature"].(string)
				assert.True(t, s != "" || sig != "", "thinking block needs summary or signature: %v", block)
			}
		}
	}
}

func TestAssistantCacheNativeDropsEmptyStreamedBlocks(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-only"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"salve"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":2}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	nm, err := a.drainSSE(context.Background(), io.NopCloser(strings.NewReader(sse)), "claude-test", noOpBus{})
	require.NoError(t, err)
	require.Len(t, nm.Content, 3)

	cache, err := a.assistantCacheNative(nm)
	require.NoError(t, err)
	require.Len(t, cache.Payload, 1)
	assertAPILegalNativePayload(t, cache.Payload)

	var got nativeMessage
	require.NoError(t, json.Unmarshal(cache.Payload[0], &got))
	require.Len(t, got.Content, 2)
	assert.Equal(t, "thinking", got.Content[0].Type)
	assert.Equal(t, "sig-only", got.Content[0].Signature)
	assert.Contains(t, string(cache.Payload[0]), `"thinking":""`)
	assert.Equal(t, "text", got.Content[1].Type)
	assert.Equal(t, "salve", got.Content[1].Text)
	assert.NotContains(t, string(cache.Payload[0]), `{"type":"text"}`)
}

func TestAssistantCacheNativeEmptiesToNoPayload(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	cache, err := a.assistantCacheNative(nativeMessage{
		Role:    "assistant",
		Content: []nativeBlock{{Type: "text"}, {Type: "thinking"}},
	})
	require.NoError(t, err)
	assert.Empty(t, cache.Payload)
}

func TestAssistantCacheNativeKeepsSignedEmptyThinking(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	cache, err := a.assistantCacheNative(nativeMessage{
		Role: "assistant",
		Content: []nativeBlock{
			{Type: "thinking", Signature: "sig"},
			{Type: "tool_use", ID: "t1", Name: "bash", Input: map[string]interface{}{"command": "ls"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, cache.Payload, 1)
	assert.Contains(t, string(cache.Payload[0]), `"thinking":""`)
	assert.Contains(t, string(cache.Payload[0]), `"signature":"sig"`)
}

func TestAssistantCacheNativeUnparsedToolInputUncacheable(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag", CacheNamespace: "anthropic"}
	cache, err := a.assistantCacheNative(nativeMessage{
		Role: "assistant",
		Content: []nativeBlock{
			{Type: "text", Text: "salve"},
			{Type: "tool_use", ID: "t1", Name: "bash", Input: `{"command":"trunc`},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, cache.Payload)
}

func TestValidNativeBlockMatchesDecoderSkip(t *testing.T) {
	blocks := []nativeBlock{
		{Type: "text"},
		{Type: "text", Text: "   "},
		{Type: "text", Text: "salve"},
		{Type: "thinking", Signature: "sig-only"},
		{Type: "thinking", Thinking: "  "},
		{Type: "thinking", Thinking: "hm", Signature: "sig"},
		{Type: "tool_use"},
		{Type: "tool_use", ID: "t1", Name: "bash", Input: map[string]interface{}{}},
		{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
		{},
	}
	for _, b := range blocks {
		decoded := decodeNativeMessage(nativeMessage{Role: "assistant", Content: []nativeBlock{b}})
		if validNativeBlock(b) {
			assert.Len(t, decoded.Content, 1, "block %+v", b)
		} else {
			assert.Empty(t, decoded.Content, "block %+v", b)
		}
	}
}

func TestCacheableNativeBlockShapesCarryRequiredFields(t *testing.T) {
	blocks := []nativeBlock{
		{Type: "text", Text: "salve"},
		{Type: "thinking", Thinking: "hm", Signature: "sig"},
		{Type: "redacted_thinking", Data: "opaque"},
		{Type: "tool_use", ID: "t1", Name: "bash", Input: map[string]interface{}{}},
	}
	for _, b := range blocks {
		require.True(t, validNativeBlock(b), "block %+v", b)
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &m))
		require.Contains(t, nativeRequiredFields, b.Type)
		for _, field := range nativeRequiredFields[b.Type] {
			assert.Contains(t, m, field, "block %+v marshaled without %q", b, field)
		}
	}
}
