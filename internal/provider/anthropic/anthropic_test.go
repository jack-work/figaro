package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

// fakeSchema is a minimal JSON-Schema-shaped value used to exercise
// nativeTool.InputSchema serialization without depending on a real tool.
func fakeSchema() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"type": "string"},
		},
	}
}

// systemSnapshot returns a chalkboard snapshot that injects the given
// text as system.prompt — the canonical source for the projection's
// system block.
func systemSnapshot(t *testing.T, text string) chalkboard.Snapshot {
	t.Helper()
	raw, err := json.Marshal(text)
	require.NoError(t, err)
	return chalkboard.Snapshot{"system.prompt": raw}
}

// TestProjectTools_Deterministic verifies that two consecutive
// projections of the same tool list produce byte-identical output.
// This is load-bearing for the tools-block cache breakpoint.
func TestProjectTools_Deterministic(t *testing.T) {
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
		{Name: "gamma", Description: "third", Parameters: fakeSchema()},
	}

	a := projectTools(tools)
	b := projectTools(tools)

	abytes, err := json.Marshal(a)
	require.NoError(t, err)
	bbytes, err := json.Marshal(b)
	require.NoError(t, err)

	assert.Equal(t, string(abytes), string(bbytes), "consecutive projectTools calls must produce equal bytes")
}

// TestProjectTools_RoundTrip verifies that projectTools output survives
// a JSON encode/decode cycle without byte-level reordering when
// re-projected. This guards against accidental introduction of a
// non-deterministic field (e.g. a map[string]interface{} schema whose
// key order depends on iteration).
func TestProjectTools_RoundTrip(t *testing.T) {
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}

	first, err := json.Marshal(projectTools(tools))
	require.NoError(t, err)

	var decoded []nativeTool
	require.NoError(t, json.Unmarshal(first, &decoded))

	second, err := json.Marshal(decoded)
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second), "projectTools output must round-trip through JSON byte-identically")
}

// TestProjectMessages_CacheBreakpoints verifies that markCacheBreakpoints
// sets cache_control on:
//   - the last system block,
//   - the last tool definition,
//   - the last content block of the second-to-last message.
func TestProjectMessages_CacheBreakpoints(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("first turn")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("first reply")}},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("second turn — current prompt")}},
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}

	req, _ := a.projectMessagesWithModel(msgs, systemSnapshot(t, "you are a test agent"), causal.Slice[message.ProviderTranslation]{}, tools, 1024, false, "claude-test")

	require.NotEmpty(t, req.System, "system must be present")
	last := req.System[len(req.System)-1]
	require.NotNil(t, last.CacheControl, "last system block must have cache_control set")
	assert.Equal(t, "ephemeral", last.CacheControl.Type)

	require.NotEmpty(t, req.Tools, "tools must be present")
	require.NotNil(t, req.Tools[len(req.Tools)-1].CacheControl, "last tool must have cache_control set")
	assert.Equal(t, "ephemeral", req.Tools[len(req.Tools)-1].CacheControl.Type)

	require.GreaterOrEqual(t, len(req.Messages), 2, "expect at least two messages for the message-level breakpoint")
	stm := req.Messages[len(req.Messages)-2]
	require.NotEmpty(t, stm.Content, "second-to-last message must have content")
	require.NotNil(t, stm.Content[len(stm.Content)-1].CacheControl, "second-to-last message's last content block must have cache_control set")
	assert.Equal(t, "ephemeral", stm.Content[len(stm.Content)-1].CacheControl.Type)

	// And: the last message (the new user prompt) must NOT have cache_control —
	// it's not yet "stable" so it shouldn't pollute the cache prefix.
	lastMsg := req.Messages[len(req.Messages)-1]
	require.NotEmpty(t, lastMsg.Content)
	assert.Nil(t, lastMsg.Content[len(lastMsg.Content)-1].CacheControl, "the leaf user prompt must not carry cache_control")
}

// TestProjectMessages_NoMessageBreakpoint_WhenSingleMessage verifies
// that the message-level breakpoint is suppressed when there is only
// one message — there is no "stable prior leaf" to anchor.
func TestProjectMessages_NoMessageBreakpoint_WhenSingleMessage(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("first prompt — nothing on disk yet")}},
	}

	req, _ := a.projectMessagesWithModel(msgs, systemSnapshot(t, "you are a test agent"), causal.Slice[message.ProviderTranslation]{}, nil, 1024, false, "claude-test")

	require.Len(t, req.Messages, 1)
	require.NotEmpty(t, req.Messages[0].Content)
	assert.Nil(t, req.Messages[0].Content[0].CacheControl, "single message must not carry cache_control")
}

// TestProjectMessages_StableAcrossCalls verifies that the same input
// produces byte-identical request output across two consecutive calls.
// Cache-prefix invariant in its weakest form (same input → same bytes).
func TestProjectMessages_StableAcrossCalls(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("ciao")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("salve")}},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("again")}},
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}

	snap := systemSnapshot(t, "you are a test agent")
	r1, _ := a.projectMessagesWithModel(msgs, snap, causal.Slice[message.ProviderTranslation]{}, tools, 1024, false, "claude-test")
	r2, _ := a.projectMessagesWithModel(msgs, snap, causal.Slice[message.ProviderTranslation]{}, tools, 1024, false, "claude-test")

	b1, err := json.Marshal(r1)
	require.NoError(t, err)
	b2, err := json.Marshal(r2)
	require.NoError(t, err)

	assert.Equal(t, string(b1), string(b2), "projectMessagesWithModel must produce byte-identical output for the same input")
}

// TestProjectMessages_OAuthSystemArray verifies that the OAuth path
// produces a two-element system array with the Claude Code identity
// prefix first, the credo (with its override preamble) second, and
// cache_control attached to the last (credo) block.
func TestProjectMessages_OAuthSystemArray(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	}

	req, _ := a.projectMessagesWithModel(msgs, systemSnapshot(t, "you are figaro"), causal.Slice[message.ProviderTranslation]{}, nil, 1024, true, "claude-test")

	require.Len(t, req.System, 2, "OAuth system must have two blocks: Claude Code identity + credo")
	assert.Contains(t, req.System[0].Text, "Claude Code")
	assert.Contains(t, req.System[1].Text, "you are figaro")
	assert.Nil(t, req.System[0].CacheControl, "first OAuth system block must not carry cache_control")
	require.NotNil(t, req.System[1].CacheControl, "last OAuth system block must carry cache_control")
}
