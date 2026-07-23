package anthropic

import (
	"context"
	"encoding/json"
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
