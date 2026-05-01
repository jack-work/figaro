package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func TestTranslation_GetSet(t *testing.T) {
	var b message.Translation
	assert.True(t, b.IsEmpty())

	b.Set("anthropic", message.ProviderTranslation{
		Messages:    []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
		Fingerprint: "abc123",
	})

	got, ok := b.Get("anthropic")
	require.True(t, ok)
	assert.Equal(t, "abc123", got.Fingerprint)
	require.Len(t, got.Messages, 1)

	_, ok = b.Get("openai")
	assert.False(t, ok, "absent provider returns ok=false")
}

func TestTranslation_RoundTrip_NewShape(t *testing.T) {
	original := message.Translation{
		Entries: map[string]message.ProviderTranslation{
			"anthropic": {
				Messages: []json.RawMessage{
					json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`),
				},
				Fingerprint: "deadbeef",
			},
			"openai": {
				Messages: []json.RawMessage{
					json.RawMessage(`{"role":"developer","content":"hi"}`),
				},
			},
		},
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.Translation
	require.NoError(t, json.Unmarshal(b, &decoded))

	got, ok := decoded.Get("anthropic")
	require.True(t, ok)
	assert.Equal(t, "deadbeef", got.Fingerprint)
	require.Len(t, got.Messages, 1)

	got2, ok := decoded.Get("openai")
	require.True(t, ok)
	assert.Empty(t, got2.Fingerprint, "missing fp serializes to empty")
}

func TestTranslation_Empty_NilOrAbsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"null", []byte("null")},
		{"empty object", []byte("{}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b message.Translation
			require.NoError(t, json.Unmarshal(tc.data, &b))
			assert.True(t, b.IsEmpty())
		})
	}
}
