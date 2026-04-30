package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

func TestMessage_Roundtrip_PlainUserMessage(t *testing.T) {
	original := message.Message{
		Role:        message.RoleUser,
		Content:     []message.Content{message.TextContent("ciao")},
		LogicalTime: 7,
		Timestamp:   1700000000000,
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, message.RoleUser, decoded.Role)
	require.Len(t, decoded.Content, 1)
	assert.Equal(t, "ciao", decoded.Content[0].Text)
	assert.Equal(t, uint64(7), decoded.LogicalTime)
	assert.Empty(t, decoded.Patches)
	assert.True(t, decoded.Baggage.IsEmpty())
}

func TestMessage_Roundtrip_WithPatches(t *testing.T) {
	original := message.Message{
		Role:        message.RoleUser,
		Content:     []message.Content{message.TextContent("explain this")},
		LogicalTime: 3,
		Timestamp:   1700000000000,
		Patches: []message.Patch{
			{
				Set: map[string]json.RawMessage{
					"cwd":      json.RawMessage(`"/home/figaro"`),
					"datetime": json.RawMessage(`"Wednesday, April 30, 2026, 9AM EDT"`),
				},
			},
		},
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	require.Len(t, decoded.Patches, 1)
	assert.Equal(t, json.RawMessage(`"/home/figaro"`), decoded.Patches[0].Set["cwd"])
}

// TestMessage_StateOnlyTic verifies that a user-role Message carrying
// only Patches (no Content) — bootstrap or rehydrate — round-trips
// correctly.
func TestMessage_StateOnlyTic(t *testing.T) {
	bootstrap := message.Message{
		Role:        message.RoleUser,
		LogicalTime: 1,
		Timestamp:   1700000000000,
		// No Content.
		Patches: []message.Patch{
			{
				Set: map[string]json.RawMessage{
					"system.prompt":            json.RawMessage(`"you are figaro"`),
					"system.model":             json.RawMessage(`"claude-opus-4-6"`),
					"system.reminder_renderer": json.RawMessage(`"tag"`),
				},
			},
		},
	}

	b, err := json.Marshal(bootstrap)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, message.RoleUser, decoded.Role)
	assert.Empty(t, decoded.Content, "state-only tic has no Content")
	require.Len(t, decoded.Patches, 1)
	assert.Equal(t, json.RawMessage(`"you are figaro"`), decoded.Patches[0].Set["system.prompt"])
}

// TestMessage_BaggageNewShape exercises the new Baggage type wired
// into Message.
func TestMessage_BaggageNewShape(t *testing.T) {
	m := message.Message{
		Role:        message.RoleAssistant,
		Content:     []message.Content{message.TextContent("ok")},
		LogicalTime: 2,
	}
	m.Baggage.Set("anthropic", message.ProviderBaggage{
		Messages:    []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)},
		Fingerprint: "fp1",
	})

	b, err := json.Marshal(m)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	pb, ok := decoded.Baggage.Get("anthropic")
	require.True(t, ok)
	assert.Equal(t, "fp1", pb.Fingerprint)
	require.Len(t, pb.Messages, 1)
}

// TestMessage_LegacyBaggage_OnDisk verifies that Messages serialized
// with the pre-A2 baggage shape (flat map[provider]json.RawMessage)
// load correctly into the new Baggage type.
func TestMessage_LegacyBaggage_OnDisk(t *testing.T) {
	// Hand-crafted legacy JSON: the baggage field is the old flat-map shape.
	legacy := []byte(`{
		"role": "assistant",
		"content": [{"type":"text","text":"ciao"}],
		"logical_time": 2,
		"timestamp": 1700000000000,
		"baggage": {"anthropic": {"role":"assistant","content":[{"type":"text","text":"ciao"}]}}
	}`)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(legacy, &decoded))

	pb, ok := decoded.Baggage.Get("anthropic")
	require.True(t, ok)
	require.Len(t, pb.Messages, 1, "legacy single blob converts to length-1 Messages slice")
	assert.Empty(t, pb.Fingerprint)
}

// TestPatch_AliasIdentity verifies the type-alias contract — a value
// constructed as chalkboard.Patch is assignable to message.Patch and
// vice versa.
func TestPatch_AliasIdentity(t *testing.T) {
	cb := chalkboard.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}}
	var m message.Patch = cb
	assert.False(t, m.IsEmpty())

	mp := message.Patch{Set: map[string]json.RawMessage{}, Remove: []string{"x"}}
	assert.False(t, chalkboard.Patch(mp).IsEmpty())
}
