package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssistantCachePreservesSignedThinking(t *testing.T) {
	var msg anthropic.Message
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-test",
		"content":[
			{"type":"thinking","thinking":"secret","signature":"sig-abc"},
			{"type":"text","text":"salve"}
		],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":2}
	}`), &msg))
	p := &Provider{reminder: "tag", CacheNamespace: "anthropic"}
	native, err := p.assistantCache(msg)
	require.NoError(t, err)
	require.Len(t, native.Payload, 1)
	raw := string(native.Payload[0])
	assert.Contains(t, raw, `"thinking":"secret"`)
	assert.Contains(t, raw, `"signature":"sig-abc"`)
	assert.NotContains(t, raw, `"stop_reason"`)
	assert.NotContains(t, raw, `"model"`)
	assert.NotContains(t, raw, `"usage"`)
	assert.Equal(t, "anthropic", native.Namespace)
	assert.Equal(t, p.Fingerprint(), native.Fingerprint)
}
