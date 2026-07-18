package anthropicsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func projectAll(t testing.TB, perMessage [][]json.RawMessage, lts []uint64) projectedMessages {
	t.Helper()
	var projected projectedMessages
	for i, encoded := range perMessage {
		var lt uint64
		if i < len(lts) {
			lt = lts[i]
		}
		projected = appendProjectedMessages(projected, encoded, lt)
	}
	require.NoError(t, projected.err)
	return projected
}

func legacyBuildParams(perMessage [][]json.RawMessage, lts []uint64, snap chalkboard.Snapshot, tools []provider.Tool, maxTokens int64, oauth bool, model string) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		MaxTokens: maxTokens,
		Model:     anthropic.Model(model),
		System:    systemBlocks(snap, oauth),
		Tools:     toolUnions(tools),
	}
	var msgLTs []uint64
	for i, entry := range perMessage {
		var lt uint64
		if i < len(lts) {
			lt = lts[i]
		}
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			var msg anthropic.MessageParam
			if err := json.Unmarshal(raw, &msg); err != nil {
				return anthropic.MessageNewParams{}, fmt.Errorf("unmarshal cached message: %w", err)
			}
			params.Messages = append(params.Messages, msg)
			msgLTs = append(msgLTs, lt)
		}
	}
	params.Messages, msgLTs = coalesceMessages(params.Messages, msgLTs)
	if setting := resolveCacheControl(snap); setting != "" {
		markCacheBreakpoints(&params, setting)
	}
	applyMessageTags(&params, msgLTs, snap)
	applyThinking(&params, snap, model)
	return params, nil
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
	assert.Equal(t, message.ContentProse, ir.Content[0].Type)
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
// leaf of the LAST input message (the rolling tail), and that earlier
// messages are not stamped.
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

	projected := projectAll(t, encodeAll(p, msgs), []uint64{1, 2, 3})
	params := buildParams(projected.Messages, projected.LogicalTimes, snap, tools, 1024, false, "claude-test")

	// System.
	require.NotEmpty(t, params.System)
	lastSys := params.System[len(params.System)-1]
	assertCacheStamp(t, lastSys.CacheControl, "last system block")

	// Tools.
	require.NotEmpty(t, params.Tools)
	lastTool := params.Tools[len(params.Tools)-1].OfTool
	require.NotNil(t, lastTool)
	assertCacheStamp(t, lastTool.CacheControl, "last tool")

	// Messages: last leaf stamped (rolling tail), second-to-last unstamped.
	require.GreaterOrEqual(t, len(params.Messages), 2)
	last := params.Messages[len(params.Messages)-1]
	require.NotEmpty(t, last.Content)
	lastLeaf := last.Content[len(last.Content)-1]
	require.NotNil(t, lastLeaf.OfText)
	assertCacheStamp(t, lastLeaf.OfText.CacheControl, "last message leaf")

	stm := params.Messages[len(params.Messages)-2]
	require.NotEmpty(t, stm.Content)
	require.NotNil(t, stm.Content[len(stm.Content)-1].OfText)
	assert.True(t, isUnstamped(stm.Content[len(stm.Content)-1].OfText.CacheControl),
		"second-to-last message must NOT carry cache_control")
}

func TestBuildParams_SingleMessageBreakpoint(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("only message")}},
	}
	snap := systemSnapshot(t, "you are a test agent")
	snap["system.cache_control"] = json.RawMessage(`"ephemeral"`)

	projected := projectAll(t, encodeAll(p, msgs), []uint64{1})
	params := buildParams(projected.Messages, projected.LogicalTimes, snap, nil, 1024, false, "claude-test")

	require.Len(t, params.Messages, 1)
	require.NotEmpty(t, params.Messages[0].Content)
	leaf := params.Messages[0].Content[0]
	require.NotNil(t, leaf.OfText)
	assertCacheStamp(t, leaf.OfText.CacheControl, "single message leaf (whole-prompt cache)")
}

// Caching is on by default (short) with no system.cache_control set, and
// "none" disables it entirely.
func TestBuildParams_CacheDefaultsOnAndNoneDisables(t *testing.T) {
	p := &Provider{}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("hi")}},
	}

	// Default: unset → caching applied.
	on := systemSnapshot(t, "agent")
	projected := projectAll(t, encodeAll(p, msgs), []uint64{1})
	params := buildParams(projected.Messages, projected.LogicalTimes, on, nil, 1024, false, "claude-test")
	require.NotEmpty(t, params.System)
	assertCacheStamp(t, params.System[len(params.System)-1].CacheControl, "default-on system block")

	// "none" → no stamps anywhere.
	off := systemSnapshot(t, "agent")
	off["system.cache_control"] = json.RawMessage(`"none"`)
	params = buildParams(projected.Messages, projected.LogicalTimes, off, nil, 1024, false, "claude-test")
	require.NotEmpty(t, params.System)
	assert.True(t, isUnstamped(params.System[len(params.System)-1].CacheControl), "none must not stamp system")
	require.NotEmpty(t, params.Messages[0].Content)
	assert.True(t, isUnstamped(params.Messages[0].Content[0].OfText.CacheControl), "none must not stamp message")
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
	projected := projectAll(t, pre, []uint64{1, 2, 3})

	r1 := buildParams(projected.Messages, projected.LogicalTimes, snap, tools, 1024, false, "claude-test")
	r2 := buildParams(projected.Messages, projected.LogicalTimes, snap, tools, 1024, false, "claude-test")

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

	projected := projectAll(t, encodeAll(p, msgs), []uint64{1})
	params := buildParams(projected.Messages, projected.LogicalTimes, snap, nil, 1024, true, "claude-test")

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
	projected := projectAll(t, pre, lts)

	snap := systemSnapshot(t, "you are a test agent")
	snap["system.cache_control"] = json.RawMessage(`"none"`) // isolate per-LT tagging from auto-breakpoints
	snap["system.tags"] = json.RawMessage(`{"11":{"cache_control":"ephemeral"}}`)

	params := buildParams(projected.Messages, projected.LogicalTimes, snap, nil, 1024, false, "claude-test")
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

func TestBuildParams_ByteIdenticalToCachedReconstruction(t *testing.T) {
	p := &Provider{}
	perMessage := encodeAll(p, []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("first user")}},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("adjacent user")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("placeholder")}},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("adjacent assistant")}},
		{Role: message.RoleUser, Content: []message.Content{{
			Type: message.ContentToolResult, ToolCallID: "toolu_signed", Text: "tool output",
		}}},
	})
	perMessage[2][0] = json.RawMessage(`{"role":"assistant","content":[{"type":"thinking","thinking":"private chain","signature":"signed-thinking"},{"type":"text","text":"calling tool"},{"type":"tool_use","id":"toolu_signed","name":"lookup","input":{"query":"figaro"}}]}`)
	lts := []uint64{10, 11, 12, 13, 14}
	projected := projectAll(t, perMessage, lts)
	immutableBefore, err := json.Marshal(projected.Messages)
	require.NoError(t, err)

	tools := []provider.Tool{{
		Name: "lookup", Description: "find a value", Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"query"},
		},
	}}
	cases := []struct {
		name   string
		snap   chalkboard.Snapshot
		oauth  bool
		model  string
		maxOut int64
	}{
		{
			name:  "cache_markers_and_tools",
			snap:  systemSnapshot(t, "cache persona"),
			model: "claude-sonnet-4-5",
		},
		{
			name: "per_lt_tags_after_coalescing",
			snap: chalkboard.Snapshot{
				"system.cache_control": json.RawMessage(`"none"`),
				"system.tags":          json.RawMessage(`{"11":{"cache_control":"1h"},"13":{"cache_control":"5m"},"14":{"cache_control":"ephemeral"}}`),
			},
			model: "claude-sonnet-4-5",
		},
		{
			name: "budget_thinking_and_signed_block",
			snap: chalkboard.Snapshot{
				"system.cache_control":   json.RawMessage(`"ephemeral"`),
				"system.thinking_budget": json.RawMessage(`2048`),
			},
			model:  "claude-sonnet-4-5",
			maxOut: 1024,
		},
		{
			name: "adaptive_thinking_model_switch",
			snap: chalkboard.Snapshot{
				"system.cache_control":   json.RawMessage(`"5m"`),
				"system.thinking_effort": json.RawMessage(`"medium"`),
			},
			model:  "claude-sonnet-4-6",
			maxOut: 8192,
		},
		{
			name:   "oauth_system_shape",
			snap:   systemSnapshot(t, "oauth persona"),
			oauth:  true,
			model:  "claude-opus-4-8",
			maxOut: 8192,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			maxOut := tc.maxOut
			if maxOut == 0 {
				maxOut = 8192
			}
			want, err := legacyBuildParams(perMessage, lts, tc.snap, tools, maxOut, tc.oauth, tc.model)
			require.NoError(t, err)
			got := buildParams(projected.Messages, projected.LogicalTimes, tc.snap, tools, maxOut, tc.oauth, tc.model)
			wantJSON, err := json.Marshal(want)
			require.NoError(t, err)
			gotJSON, err := json.Marshal(got)
			require.NoError(t, err)
			assert.Equal(t, string(wantJSON), string(gotJSON))
		})
	}

	immutableAfter, err := json.Marshal(projected.Messages)
	require.NoError(t, err)
	assert.Equal(t, string(immutableBefore), string(immutableAfter), "request-local changes mutated the parsed projection")
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
