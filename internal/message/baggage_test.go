package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func TestBaggage_GetSet(t *testing.T) {
	var b message.Baggage
	assert.True(t, b.IsEmpty())

	b.Set("anthropic", message.ProviderBaggage{
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

func TestBaggage_RoundTrip_NewShape(t *testing.T) {
	original := message.Baggage{
		Entries: map[string]message.ProviderBaggage{
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

	var decoded message.Baggage
	require.NoError(t, json.Unmarshal(b, &decoded))

	got, ok := decoded.Get("anthropic")
	require.True(t, ok)
	assert.Equal(t, "deadbeef", got.Fingerprint)
	require.Len(t, got.Messages, 1)

	got2, ok := decoded.Get("openai")
	require.True(t, ok)
	assert.Empty(t, got2.Fingerprint, "missing fp serializes to empty")
}

// TestBaggage_BackCompat_LegacyShape verifies that pre-Stage-A2
// serialized baggage (a flat map of provider name → single raw JSON
// blob) reads correctly into the new Baggage shape.
func TestBaggage_BackCompat_LegacyShape(t *testing.T) {
	legacy := []byte(`{"anthropic":{"role":"assistant","content":[{"type":"text","text":"ciao"}]}}`)

	var decoded message.Baggage
	require.NoError(t, json.Unmarshal(legacy, &decoded))

	got, ok := decoded.Get("anthropic")
	require.True(t, ok)
	require.Len(t, got.Messages, 1, "legacy single blob converts to length-1 Messages slice")
	assert.Empty(t, got.Fingerprint, "legacy entries have no fingerprint")

	// The wire message should be the original blob unchanged.
	var native struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(got.Messages[0], &native))
	assert.Equal(t, "assistant", native.Role)
	require.Len(t, native.Content, 1)
	assert.Equal(t, "ciao", native.Content[0].Text)
}

func TestBaggage_BackCompat_MultipleProviders(t *testing.T) {
	legacy := []byte(`{"anthropic":{"a":1},"openai":{"b":2}}`)

	var decoded message.Baggage
	require.NoError(t, json.Unmarshal(legacy, &decoded))

	a, ok := decoded.Get("anthropic")
	require.True(t, ok)
	require.Len(t, a.Messages, 1)

	o, ok := decoded.Get("openai")
	require.True(t, ok)
	require.Len(t, o.Messages, 1)
}

func TestBaggage_Empty_NilOrAbsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"null", []byte("null")},
		{"empty object", []byte("{}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b message.Baggage
			require.NoError(t, json.Unmarshal(tc.data, &b))
			assert.True(t, b.IsEmpty())
		})
	}
}

// TestBaggage_NewShape_RewriteAfterRead confirms the migration
// invariant: read legacy → write current. After unmarshal+remarshal,
// the wire shape is the new one.
func TestBaggage_NewShape_RewriteAfterRead(t *testing.T) {
	legacy := []byte(`{"anthropic":{"role":"assistant"}}`)
	var b message.Baggage
	require.NoError(t, json.Unmarshal(legacy, &b))

	out, err := json.Marshal(b)
	require.NoError(t, err)

	// Output should be the new shape, with "entries" wrapper.
	var checkNewShape struct {
		Entries map[string]json.RawMessage `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(out, &checkNewShape))
	require.NotNil(t, checkNewShape.Entries, "remarshal uses the new shape with \"entries\" key")
	require.Contains(t, checkNewShape.Entries, "anthropic")
}
