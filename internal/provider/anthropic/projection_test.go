package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/message"
)

// TestProjectMessages_IgnoresNonMessageCachedEntries guards against
// regression of the Stage E bug: a translation log entry that
// represents the system block array (stored at the bootstrap flt's
// position) would unmarshal lax-style as a nativeMessage with empty
// Role, which the Anthropic API rejects with
//   messages.0.role: Input should be 'user', 'assistant'
//
// projectMessages must validate the cached entry's role before
// using it; non-message entries fall through to fresh rendering.
func TestProjectMessages_IgnoresNonMessageCachedEntries(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag"}

	// Two figaro messages: a bootstrap-style state-only tic, then
	// a real user-content tic. The cached translation at index 0
	// is system-block-shaped (no role field) — the kind of entry
	// the agent's persistProjectionSummary writes for SystemFLT.
	msgs := []message.Message{
		{Role: message.RoleUser, LogicalTime: 1}, // bootstrap, no Content, no Patches relevant here
		{Role: message.RoleUser, LogicalTime: 2,
			Content: []message.Content{message.TextContent("hello")}},
	}
	prior := causal.Wrap([]message.ProviderTranslation{
		{
			Messages: []json.RawMessage{
				json.RawMessage(`{"type":"text","text":"system block content"}`),
			},
		},
		{}, // empty for the real user tic; renders fresh
	})

	wire, _ := a.projectMessages(msgs, prior)

	// Exactly one wire message: the user prompt. The bootstrap tic
	// emits nothing (no Content), and the system-shaped cached
	// entry was rejected.
	require.Len(t, wire, 1, "system-block-shaped cached entries must not be admitted as messages")
	assert.Equal(t, "user", wire[0].Role, "every wire message must have a valid role")
	require.NotEmpty(t, wire[0].Content)
}

// TestProjectMessages_AcceptsValidCachedEntries confirms the
// happy-path cache hit still works: a properly role-bearing cached
// entry is reused verbatim.
func TestProjectMessages_AcceptsValidCachedEntries(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag"}
	msgs := []message.Message{
		{Role: message.RoleAssistant, LogicalTime: 5,
			Content: []message.Content{message.TextContent("ignored on cache hit")}},
	}
	cachedBytes := json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"cached reply"}]}`)
	prior := causal.Wrap([]message.ProviderTranslation{
		{Messages: []json.RawMessage{cachedBytes}},
	})

	wire, perFLT := a.projectMessages(msgs, prior)
	require.Len(t, wire, 1)
	assert.Equal(t, "assistant", wire[0].Role)
	require.Len(t, wire[0].Content, 1)
	assert.Equal(t, "cached reply", wire[0].Content[0].Text)
	assert.Equal(t, []byte(cachedBytes), []byte(perFLT[0]),
		"cache-hit path must reuse the cached bytes verbatim")
}
