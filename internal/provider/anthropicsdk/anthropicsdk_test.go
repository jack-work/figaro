package anthropicsdk

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
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

func systemSnapshot(t *testing.T, text string) chalkboard.Snapshot {
	t.Helper()
	raw, err := json.Marshal(text)
	require.NoError(t, err)
	return chalkboard.Snapshot{"system.credo": raw}
}

// encodeAll mirrors the agent's catchUp: encode each IR message into
// per-message wire bytes.
func encodeAll(p *Provider, msgs []message.Message) [][]json.RawMessage {
	out := make([][]json.RawMessage, 0, len(msgs))
	prevSnap := chalkboard.Snapshot{}
	for _, msg := range msgs {
		mp, ok := p.renderMessage(msg, &prevSnap)
		if !ok {
			continue
		}
		raw, err := json.Marshal(mp)
		if err != nil {
			continue
		}
		out = append(out, []json.RawMessage{raw})
	}
	return out
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	p := &Provider{}
	cases := []struct {
		name string
		ir   message.Message
	}{
		{
			"text_assistant",
			message.Message{
				Role:    message.RoleAssistant,
				Content: []message.Content{message.TextContent("Hello, world!")},
			},
		},
		{
			"mixed_assistant",
			message.Message{
				Role: message.RoleAssistant,
				Content: []message.Content{
					{Type: message.ContentThinking, Text: "Let me check the files."},
					message.TextContent("Listing now."),
					{
						Type: message.ContentToolInvoke, ToolCallID: "toolu_abc",
						ToolName:  "bash",
						Arguments: map[string]interface{}{"command": "ls -la"},
					},
				},
			},
		},
		{
			"tool_result_user",
			message.Message{
				Role: message.RoleUser,
				Content: []message.Content{{
					Type:       message.ContentToolResult,
					ToolCallID: "toolu_abc",
					Text:       "total 0\n-rw-r--r-- 1 me me 0 file",
				}},
			},
		},
		{
			// A tool_use with no arguments must still emit input:{} —
			// the API rejects missing/null and the WAL drops empty maps.
			"empty_args_tool_call",
			message.Message{
				Role: message.RoleAssistant,
				Content: []message.Content{{
					Type: message.ContentToolInvoke, ToolCallID: "toolu_empty",
					ToolName: "edit",
				}},
			},
		},
		{
			"multi_text_user",
			message.Message{
				Role: message.RoleUser,
				Content: []message.Content{
					message.TextContent("first"),
					message.TextContent("second"),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// IR -> wire is deterministic; capture once.
			encoded, err := p.encode(tc.ir, chalkboard.Snapshot{})
			require.NoError(t, err)
			require.Len(t, encoded, 1)
			wire := encoded[0]

			// Wire -> SDK MessageParam -> wire must be byte-identical.
			var mp anthropic.MessageParam
			require.NoError(t, json.Unmarshal(wire, &mp))
			roundtrip, err := json.Marshal(mp)
			require.NoError(t, err)
			assert.Equal(t, string(wire), string(roundtrip), "wire bytes must survive a MessageParam round-trip")

			// Optional: golden file snapshot for human inspection.
			gold := filepath.Join("testdata", tc.name+".json")
			if b, ferr := os.ReadFile(gold); ferr == nil {
				assert.Equal(t, string(bytes.TrimRight(b, "\n")), string(wire), "golden file mismatch (delete to regenerate)")
			} else if os.IsNotExist(ferr) && os.Getenv("UPDATE_GOLDEN") == "1" {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(gold, append(wire, '\n'), 0o644))
			}
		})
	}
}

// TestDecodeAssistantMessage_ToolUse verifies that decoding an SDK
// response with a tool_use block produces an IR with Arguments
// populated from the raw JSON.
func TestDecodeAssistantMessage_ToolUse(t *testing.T) {
	wire := `{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-test",
		"content":[
			{"type":"text","text":"thinking..."},
			{"type":"tool_use","id":"toolu_abc","name":"bash","input":{"command":"ls"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`
	var msg anthropic.Message
	require.NoError(t, msg.UnmarshalJSON([]byte(wire)))

	ir := decodeAssistantMessage(msg)
	assert.Equal(t, message.RoleAssistant, ir.Role)
	require.Len(t, ir.Content, 2)
	assert.Equal(t, message.ContentText, ir.Content[0].Type)
	assert.Equal(t, message.ContentToolInvoke, ir.Content[1].Type)
	assert.Equal(t, "toolu_abc", ir.Content[1].ToolCallID)
	assert.Equal(t, "bash", ir.Content[1].ToolName)
	assert.Equal(t, "ls", ir.Content[1].Arguments["command"])
	assert.Equal(t, message.StopToolInvoke, ir.StopReason)
	require.NotNil(t, ir.Usage)
	assert.Equal(t, 10, ir.Usage.InputTokens)
	assert.Equal(t, 5, ir.Usage.OutputTokens)
}

// TestBuildParams_CacheBreakpoints verifies that markCacheBreakpoints
// sets cache_control on the last system block, the last tool, and the
// leaf of the second-to-last message.
func TestBuildParams_CacheBreakpoints(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("first turn")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("first reply")}},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("second turn")}},
	}
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}
	snap := systemSnapshot(t, "you are a test agent")
	snap["system.cache_control"] = json.RawMessage(`"ephemeral"`)

	params, err := buildParams(encodeAll(p, msgs), []uint64{1, 2, 3}, snap, tools, 1024, false, "claude-test")
	require.NoError(t, err)

	// System.
	require.NotEmpty(t, params.System)
	lastSys := params.System[len(params.System)-1]
	assertCacheStamp(t, lastSys.CacheControl, "last system block")

	// Tools.
	require.NotEmpty(t, params.Tools)
	lastTool := params.Tools[len(params.Tools)-1].OfTool
	require.NotNil(t, lastTool)
	assertCacheStamp(t, lastTool.CacheControl, "last tool")

	// Messages: second-to-last leaf stamped, leaf message unstamped.
	require.GreaterOrEqual(t, len(params.Messages), 2)
	stm := params.Messages[len(params.Messages)-2]
	require.NotEmpty(t, stm.Content)
	stmLeaf := stm.Content[len(stm.Content)-1]
	require.NotNil(t, stmLeaf.OfText)
	assertCacheStamp(t, stmLeaf.OfText.CacheControl, "second-to-last message leaf")

	last := params.Messages[len(params.Messages)-1]
	require.NotEmpty(t, last.Content)
	require.NotNil(t, last.Content[len(last.Content)-1].OfText)
	assert.True(t, isUnstamped(last.Content[len(last.Content)-1].OfText.CacheControl),
		"leaf user prompt must NOT carry cache_control")
}

func TestBuildParams_NoMessageBreakpoint_WhenSingleMessage(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("only message")}},
	}
	snap := systemSnapshot(t, "you are a test agent")
	snap["system.cache_control"] = json.RawMessage(`"ephemeral"`)

	params, err := buildParams(encodeAll(p, msgs), []uint64{1}, snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)

	require.Len(t, params.Messages, 1)
	require.NotEmpty(t, params.Messages[0].Content)
	leaf := params.Messages[0].Content[0]
	require.NotNil(t, leaf.OfText)
	assert.True(t, isUnstamped(leaf.OfText.CacheControl), "single message must not carry cache_control")
}

func TestBuildParams_StableAcrossCalls(t *testing.T) {
	p := &Provider{}
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
	pre := encodeAll(p, msgs)

	r1, err := buildParams(pre, []uint64{1, 2, 3}, snap, tools, 1024, false, "claude-test")
	require.NoError(t, err)
	r2, err := buildParams(pre, []uint64{1, 2, 3}, snap, tools, 1024, false, "claude-test")
	require.NoError(t, err)

	b1, err := json.Marshal(r1)
	require.NoError(t, err)
	b2, err := json.Marshal(r2)
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2), "buildParams must produce byte-identical output for the same input")
}

func TestBuildParams_OAuthSystemArray(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	}
	snap := systemSnapshot(t, "you are figaro")
	snap["system.cache_control"] = json.RawMessage(`"ephemeral"`)

	params, err := buildParams(encodeAll(p, msgs), []uint64{1}, snap, nil, 1024, true, "claude-test")
	require.NoError(t, err)

	require.Len(t, params.System, 2, "OAuth system must have two blocks: Claude Code identity + credo")
	assert.Contains(t, params.System[0].Text, "Claude Code")
	assert.Contains(t, params.System[1].Text, "you are figaro")
	assert.True(t, isUnstamped(params.System[0].CacheControl), "first OAuth system block must not carry cache_control")
	assertCacheStamp(t, params.System[1].CacheControl, "last OAuth system block")
}

func TestBuildParams_PerLTTag(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("turn one")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("reply one")}},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("turn two")}},
	}
	pre := encodeAll(p, msgs)
	lts := []uint64{10, 11, 12}

	snap := systemSnapshot(t, "you are a test agent")
	snap["system.tags"] = json.RawMessage(`{"11":{"cache_control":"ephemeral"}}`)

	params, err := buildParams(pre, lts, snap, nil, 1024, false, "claude-test")
	require.NoError(t, err)
	require.Len(t, params.Messages, 3)

	tagged := params.Messages[1]
	require.NotEmpty(t, tagged.Content)
	leaf := tagged.Content[len(tagged.Content)-1]
	require.NotNil(t, leaf.OfText)
	assertCacheStamp(t, leaf.OfText.CacheControl, "LT 11")

	for _, idx := range []int{0, 2} {
		m := params.Messages[idx]
		require.NotEmpty(t, m.Content)
		l := m.Content[0]
		require.NotNil(t, l.OfText)
		assert.True(t, isUnstamped(l.OfText.CacheControl), "untagged message must not carry cache_control")
	}
}

// TestToolUnions_RoundTrip checks projectTools-equivalent stability.
func TestToolUnions_RoundTrip(t *testing.T) {
	tools := []provider.Tool{
		{Name: "alpha", Description: "first", Parameters: fakeSchema()},
		{Name: "beta", Description: "second", Parameters: fakeSchema()},
	}
	a := toolUnions(tools)
	b := toolUnions(tools)
	abytes, err := json.Marshal(a)
	require.NoError(t, err)
	bbytes, err := json.Marshal(b)
	require.NoError(t, err)
	assert.Equal(t, string(abytes), string(bbytes), "consecutive toolUnions calls must marshal byte-identically")
}

// isUnstamped reports whether a CacheControlEphemeralParam was left
// at its zero value. The SDK's `default:"ephemeral"` tag means a
// zero struct still marshals to `{"type":"ephemeral"}` when serialized
// standalone, but param.IsOmitted on the parent field check is the
// canonical signal: an omitzero+IsOmitted struct gets dropped by the
// shadow marshaller, so the field disappears from the request body.
func isUnstamped(cc anthropic.CacheControlEphemeralParam) bool {
	return param.IsOmitted(cc)
}

func assertCacheStamp(t *testing.T, cc anthropic.CacheControlEphemeralParam, label string) {
	t.Helper()
	require.False(t, param.IsOmitted(cc), "%s must carry cache_control", label)
}
