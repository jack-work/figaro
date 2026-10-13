package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func fakeSchema() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"type": "string"},
		},
	}
}

func withKey(s form.Snapshot, key string, v json.RawMessage) form.Snapshot {
	return s.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{key: v}, nil))
}

func systemSnapshot(t *testing.T, text string) form.Snapshot {
	t.Helper()
	raw, err := json.Marshal(text)
	require.NoError(t, err)
	return form.FromMap(map[string]json.RawMessage{"system.credo": raw})
}

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

func TestProjectMessages_CacheBreakpoints(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first turn")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("first reply")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("second turn: current prompt")}},
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}

	snap := systemSnapshot(t, "you are a test agent")
	snap = withKey(snap, "system.cache_control", json.RawMessage(`"ephemeral"`))
	req, _ := a.projectMessagesWithModel(a.encodeAll(msgs), snap, tools, 1024, false, "claude-test")

	require.NotEmpty(t, req.System, "system must be present")
	last := req.System[len(req.System)-1]
	require.NotNil(t, last.CacheControl, "last system block must have cache_control set")
	assert.Equal(t, "ephemeral", last.CacheControl.Type)

	require.NotEmpty(t, req.Tools, "tools must be present")
	require.NotNil(t, req.Tools[len(req.Tools)-1].CacheControl, "last tool must have cache_control set")
	assert.Equal(t, "ephemeral", req.Tools[len(req.Tools)-1].CacheControl.Type)

	require.GreaterOrEqual(t, len(req.Messages), 2, "expect at least two messages")
	stm := req.Messages[len(req.Messages)-1]
	require.NotEmpty(t, rowMessage(stm).Content, "tail message must have content")
	require.NotNil(t, rowMessage(stm).Content[len(rowMessage(stm).Content)-1].CacheControl, "the tail message's last content block must have cache_control set")
	assert.Equal(t, "ephemeral", rowMessage(stm).Content[len(rowMessage(stm).Content)-1].CacheControl.Type)

}

func TestProjectMessages_SingleMessageStillMarksTheTail(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first prompt: nothing on disk yet")}},
	}

	req, _ := a.projectMessagesWithModel(a.encodeAll(msgs), systemSnapshot(t, "you are a test agent"), nil, 1024, false, "claude-test")

	require.Len(t, req.Messages, 1)
	require.NotEmpty(t, rowMessage(req.Messages[0]).Content)
	assert.NotNil(t, rowMessage(req.Messages[0]).Content[0].CacheControl, "the opening turn must write the cache")
}

func TestProjectMessages_StableAcrossCalls(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("ciao")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("salve")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("again")}},
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}

	snap := systemSnapshot(t, "you are a test agent")
	pre := a.encodeAll(msgs)
	r1, _ := a.projectMessagesWithModel(pre, snap, tools, 1024, false, "claude-test")
	r2, _ := a.projectMessagesWithModel(pre, snap, tools, 1024, false, "claude-test")

	b1, err := json.Marshal(r1)
	require.NoError(t, err)
	b2, err := json.Marshal(r2)
	require.NoError(t, err)

	assert.Equal(t, string(b1), string(b2), "projectMessagesWithModel must produce byte-identical output for the same input")
}

func TestProjectMessages_OAuthSystemArray(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}},
	}

	snap := systemSnapshot(t, "you are figaro")
	snap = withKey(snap, "system.cache_control", json.RawMessage(`"ephemeral"`))
	req, _ := a.projectMessagesWithModel(a.encodeAll(msgs), snap, nil, 1024, true, "claude-test")

	require.Len(t, req.System, 2, "OAuth system must have two blocks: Claude Code identity + credo")
	assert.Contains(t, req.System[0].Text, "Claude Code")
	assert.Contains(t, req.System[1].Text, "you are figaro")
	assert.Nil(t, req.System[0].CacheControl, "first OAuth system block must not carry cache_control")
	require.NotNil(t, req.System[1].CacheControl, "last OAuth system block must carry cache_control")
}

func TestProjectMessages_PerLTTag(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("turn one")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("reply one")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("turn two")}},
	}
	pre := a.encodeAll(msgs)
	lts := []uint64{10, 11, 12}

	snap := systemSnapshot(t, "you are a test agent")
	snap = withKey(snap, "system.tags", json.RawMessage(`{"11":{"cache_control":"ephemeral"}}`))

	req, err := a.projectMessagesWithLTs(pre, lts, snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)
	require.Len(t, req.Messages, 3)

	tagged := req.Messages[1]
	require.NotEmpty(t, rowMessage(tagged).Content)
	require.NotNil(t, rowMessage(tagged).Content[len(rowMessage(tagged).Content)-1].CacheControl,
		"message at LT 11 must carry cache_control from system.tags")
	assert.Equal(t, "ephemeral", rowMessage(tagged).Content[len(rowMessage(tagged).Content)-1].CacheControl.Type)

	assert.Nil(t, rowMessage(req.Messages[0]).Content[0].CacheControl)
	assert.NotNil(t, rowMessage(req.Messages[2]).Content[0].CacheControl, "the rolling tail marker is automatic")
	assert.LessOrEqual(t, countCacheMarkers(req), provider.MaxCacheBreakpoints,
		"per-LT tags layer on top of the automatic markers and must not breach the API cap")
}

func TestCachingIsOnByDefault(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first turn")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("first reply")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("second turn")}},
	}
	tools := []provider.Tool{{Name: "alpha", Description: "first", Parameters: fakeSchema()}}

	snap := systemSnapshot(t, "you are a test agent")
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), snap, tools, 1024, false, "claude-test")
	require.NoError(t, err)

	require.NotEmpty(t, req.System)
	assert.NotNil(t, req.System[len(req.System)-1].CacheControl,
		"default aria must mark the system prefix")
	assert.NotNil(t, req.Tools[len(req.Tools)-1].CacheControl,
		"default aria must mark the tool prefix")
	assert.Greater(t, countCacheMarkers(req), 0, "default aria must SIGNAL caching (markers are all figaro controls; whether the provider then caches is its decision)")
}

func TestCachingOffIsHonoured(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("only turn")}},
	}
	snap := withKey(systemSnapshot(t, "you are a test agent"), "system.cache_control", json.RawMessage(`"none"`))
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)
	assert.Equal(t, 0, countCacheMarkers(req), `system.cache_control "none" must mark nothing`)
}

func TestNeverExceedsTheAPIMarkerCap(t *testing.T) {
	a := &Anthropic{}
	var msgs []message.Message
	for i := 0; i < 12; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		msgs = append(msgs, message.Message{Role: role, Content: []message.Content{message.TextContent("turn")}})
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "a", Parameters: fakeSchema()},
		{Name: "beta", Description: "b", Parameters: fakeSchema()},
	}
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), systemSnapshot(t, "credo"), tools, 1024, false, "claude-test")
	require.NoError(t, err)
	assert.LessOrEqual(t, countCacheMarkers(req), provider.AutoCacheBreakpoints,
		"figaro must emit at most 3 markers so a downstream gateway can top up within Anthropic's cap of 4")
}

func TestTTLIsAFieldNotAType(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("turn")}},
	}
	snap := withKey(systemSnapshot(t, "credo"), "system.cache_control", json.RawMessage(`"1h"`))
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)

	cc := req.System[len(req.System)-1].CacheControl
	require.NotNil(t, cc)
	assert.Equal(t, "ephemeral", cc.Type, "type is an enum of exactly one value")
	assert.Equal(t, "1h", cc.TTL, "retention rides in ttl")

	raw, err := json.Marshal(cc)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, string(raw))
}

func TestPerLTTagNoneDisablesRatherThanEmittingGarbage(t *testing.T) {
	a := &Anthropic{}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("one")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("two")}},
	}
	snap := withKey(systemSnapshot(t, "credo"), "system.tags", json.RawMessage(`{"10":{"cache_control":"none"}}`))
	req, err := a.projectMessagesWithLTs(a.encodeAll(msgs), []uint64{10, 11}, snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)
	assert.Nil(t, rowMessage(req.Messages[0]).Content[0].CacheControl, `a "none" tag must not stamp a marker`)
}

func TestTTLDroppedOnARouteThatDoesNotHonourIt(t *testing.T) {
	a := &Anthropic{Route: provider.GatewayRoute("http://127.0.0.1:61890/v1")}
	msgs := []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("turn")}},
	}
	snap := withKey(systemSnapshot(t, "credo"), "system.cache_control", json.RawMessage(`"1h"`))
	snap = withKey(snap, provider.CacheMarkersKey, json.RawMessage(`"blocks"`))
	req, err := a.projectMessagesWithModel(a.encodeAll(msgs), snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)
	cc := req.System[len(req.System)-1].CacheControl
	require.NotNil(t, cc)
	assert.Equal(t, "ephemeral", cc.Type)
	assert.Empty(t, cc.TTL, "a route without TTL support must not be sent one")
}
