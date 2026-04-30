package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

func TestLogEntry_MessageOnly_Roundtrip(t *testing.T) {
	original := message.LogEntry{
		LogicalTime: 7,
		Timestamp:   1700000000000,
		Message: &message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{message.TextContent("ciao")},
		},
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.LogEntry
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, uint64(7), decoded.LogicalTime)
	assert.Equal(t, int64(1700000000000), decoded.Timestamp)
	require.NotNil(t, decoded.Message)
	assert.Equal(t, message.RoleUser, decoded.Message.Role)
	assert.Nil(t, decoded.Patch)
	assert.True(t, decoded.IsMessageOnly())
	assert.False(t, decoded.IsPatchOnly())
	assert.False(t, decoded.HasSidecar())
}

func TestLogEntry_PatchOnly_Roundtrip(t *testing.T) {
	original := message.LogEntry{
		LogicalTime: 1,
		Timestamp:   1700000000000,
		Patch: &message.Patch{
			Set: map[string]json.RawMessage{
				"system.prompt": json.RawMessage(`"you are figaro"`),
				"system.model":  json.RawMessage(`"claude-opus-4-6"`),
			},
		},
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.LogEntry
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Nil(t, decoded.Message)
	require.NotNil(t, decoded.Patch)
	assert.Equal(t, json.RawMessage(`"you are figaro"`), decoded.Patch.Set["system.prompt"])
	assert.True(t, decoded.IsPatchOnly())
	assert.False(t, decoded.IsMessageOnly())
	assert.False(t, decoded.HasSidecar())
}

func TestLogEntry_Sidecar_Roundtrip(t *testing.T) {
	original := message.LogEntry{
		LogicalTime: 3,
		Timestamp:   1700000000000,
		Message: &message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{message.TextContent("explain this")},
		},
		Patch: &message.Patch{
			Set: map[string]json.RawMessage{
				"cwd": json.RawMessage(`"/home/figaro"`),
			},
		},
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.LogEntry
	require.NoError(t, json.Unmarshal(b, &decoded))

	require.NotNil(t, decoded.Message)
	require.NotNil(t, decoded.Patch)
	assert.True(t, decoded.HasSidecar())
}

// TestPatch_AliasIdentity verifies that message.Patch and
// chalkboard.Patch are the SAME TYPE — values constructed via one
// path are usable via the other without conversion. This is the
// type-alias contract.
func TestPatch_AliasIdentity(t *testing.T) {
	cb := chalkboard.Patch{Set: map[string]json.RawMessage{
		"k": json.RawMessage(`"v"`),
	}}
	// If they're the same type, this assignment compiles.
	var m message.Patch = cb
	assert.False(t, m.IsEmpty())
	// And the reverse: methods on chalkboard.Patch work on a
	// message.Patch value.
	mp := message.Patch{Set: map[string]json.RawMessage{}, Remove: []string{"x"}}
	assert.False(t, chalkboard.Patch(mp).IsEmpty())
}

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

func TestBaggage_RoundTrip(t *testing.T) {
	original := message.Baggage{
		Entries: map[string]message.ProviderBaggage{
			"anthropic": {
				Messages:    []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`)},
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

func TestLogEntry_WithBaggage_Roundtrip(t *testing.T) {
	original := message.LogEntry{
		LogicalTime: 5,
		Timestamp:   1700000000000,
		Message: &message.Message{
			Role:    message.RoleUser,
			Content: []message.Content{message.TextContent("hello")},
		},
	}
	original.Baggage.Set("anthropic", message.ProviderBaggage{
		Messages:    []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
		Fingerprint: "fp1",
	})

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.LogEntry
	require.NoError(t, json.Unmarshal(b, &decoded))

	bg, ok := decoded.Baggage.Get("anthropic")
	require.True(t, ok)
	assert.Equal(t, "fp1", bg.Fingerprint)
}
