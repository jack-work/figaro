package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func TestProviderTranslation_IsEmpty(t *testing.T) {
	var pt message.ProviderTranslation
	assert.True(t, pt.IsEmpty())

	pt.Messages = []json.RawMessage{json.RawMessage(`{"role":"user"}`)}
	assert.False(t, pt.IsEmpty())
}

func TestProviderTranslation_RoundTrip(t *testing.T) {
	original := message.ProviderTranslation{
		Messages: []json.RawMessage{
			json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`),
		},
		Fingerprint: "deadbeef",
	}
	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.ProviderTranslation
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, "deadbeef", decoded.Fingerprint)
	require.Len(t, decoded.Messages, 1)
}

func TestProviderTranslation_FingerprintOmittedWhenEmpty(t *testing.T) {
	pt := message.ProviderTranslation{
		Messages: []json.RawMessage{json.RawMessage(`{}`)},
	}
	b, err := json.Marshal(pt)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"fp"`,
		"missing fingerprint must serialize via omitempty, not as empty string")
}
