package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

func TestMessage_Roundtrip_PlainUserMessage(t *testing.T) {
	original := message.Message{
		Role:        message.RoleInput,
		Content:     []message.Content{message.TextContent("ciao")},
		LogicalTime: 7,
		Timestamp:   1700000000000,
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, message.RoleInput, decoded.Role)
	require.Len(t, decoded.Content, 1)
	assert.Equal(t, "ciao", decoded.Content[0].Text)
	assert.Equal(t, uint64(7), decoded.LogicalTime)
	assert.Empty(t, decoded.Patches)
}

func TestMessage_Roundtrip_WithPatches(t *testing.T) {
	original := message.Message{
		Role:        message.RoleInput,
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
// only Patches (no Content) — e.g. the first-turn env-var seed or a
// `figaro set` — round-trips correctly.
func TestMessage_StateOnlyTic(t *testing.T) {
	tic := message.Message{
		Role:        message.RoleInput,
		LogicalTime: 1,
		Timestamp:   1700000000000,
		// No Content.
		Patches: []message.Patch{
			{
				Set: map[string]json.RawMessage{
					"system.credo":             json.RawMessage(`"you are figaro"`),
					"system.model":             json.RawMessage(`"claude-opus-4-6"`),
					"system.reminder_renderer": json.RawMessage(`"tag"`),
				},
			},
		},
	}

	b, err := json.Marshal(tic)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, message.RoleInput, decoded.Role)
	assert.Empty(t, decoded.Content, "state-only tic has no Content")
	require.Len(t, decoded.Patches, 1)
	assert.Equal(t, json.RawMessage(`"you are figaro"`), decoded.Patches[0].Set["system.credo"])
}

// TestPatch_AliasIdentity verifies the type-alias contract — a value
// constructed as form.Patch is assignable to message.Patch and
// vice versa.
func TestPatch_AliasIdentity(t *testing.T) {
	cb := form.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}}
	var m message.Patch = cb
	assert.False(t, m.IsEmpty())

	mp := message.Patch{Set: map[string]json.RawMessage{}, Remove: []string{"x"}}
	assert.False(t, form.Patch(mp).IsEmpty())
}

func TestNewInterruptSentinel_NamesAllToolCalls(t *testing.T) {
	calls := []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "tc_a", ToolName: "bash"},
		{Type: message.ContentToolInvoke, ToolCallID: "tc_b", ToolName: "read"},
		// Non-tool_call blocks must be ignored.
		message.TextContent("commentary"),
	}
	sentinel := message.NewInterruptSentinel(message.InterruptAgentExit, "agent exited mid-tool-use", calls)

	assert.Equal(t, message.RoleSystemInterrupt, sentinel.Role)
	require.Len(t, sentinel.Content, 2)
	assert.Equal(t, message.ContentInterrupt, sentinel.Content[0].Type)
	assert.Equal(t, "tc_a", sentinel.Content[0].ToolCallID)
	assert.Equal(t, message.InterruptAgentExit, sentinel.Content[0].Reason)
	assert.Equal(t, "agent exited mid-tool-use", sentinel.Content[0].Text)
	assert.Equal(t, "tc_b", sentinel.Content[1].ToolCallID)

	assert.True(t, message.IsInterruptSentinel(sentinel))
	assert.Equal(t, []string{"tc_a", "tc_b"}, message.DanglingToolCallIDs(sentinel))
}

func TestInterruptSentinel_NonSentinel_NoIDs(t *testing.T) {
	user := message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}}
	assert.False(t, message.IsInterruptSentinel(user))
	assert.Nil(t, message.DanglingToolCallIDs(user))
}

func TestInterruptSentinel_Roundtrip(t *testing.T) {
	original := message.NewInterruptSentinel(
		message.InterruptUserInterrupt,
		"user pressed Ctrl-C",
		[]message.Content{
			{Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "bash"},
		},
	)
	original.LogicalTime = 12
	original.Timestamp = 1700000000000

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded message.Message
	require.NoError(t, json.Unmarshal(b, &decoded))

	assert.Equal(t, message.RoleSystemInterrupt, decoded.Role)
	require.Len(t, decoded.Content, 1)
	assert.Equal(t, "tc_1", decoded.Content[0].ToolCallID)
	assert.Equal(t, message.InterruptUserInterrupt, decoded.Content[0].Reason)
	assert.Equal(t, uint64(12), decoded.LogicalTime)
}

func TestCountMessages_ExcludesCeremonial(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleGenesis}, // ceremonial (genesis)
		{Role: message.RoleInput},   // ceremonial (empty outfit birth)
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("u1")}},                        // counts
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("a1")}},                       // counts
		{Role: message.RoleInput, Content: []message.Content{message.ToolResultContent("c", "t", "ok", false)}}, // counts (tool result tic)
		{Role: message.RoleInput, Content: []message.Content{{Type: message.ContentProse, Text: ""}}},           // empty prose -> ceremonial
	}
	assert.Equal(t, 3, message.CountMessages(msgs))
	assert.True(t, message.IsCeremonial(msgs[0]))
	assert.True(t, message.IsCeremonial(msgs[1]))
	assert.False(t, message.IsCeremonial(msgs[2]))
	assert.True(t, message.IsCeremonial(msgs[5]))
}

// A store written before the input/output rename must keep reading. The
// mapping lives on the type, so no decode path can forget it — the failure it
// prevents is silent: a turn rendering under the wrong speaker.
func TestRole_LegacyVocabularyStillDecodes(t *testing.T) {
	for _, c := range []struct{ on, want string }{
		{`"user"`, "input"},
		{`"assistant"`, "output"},
		{`"input"`, "input"},   // already migrated
		{`"output"`, "output"}, // already migrated
		{`"tool_result"`, "tool_result"},
		{`"system.interrupt"`, "system.interrupt"},
		{`"genesis"`, "genesis"},
	} {
		var r message.Role
		if err := json.Unmarshal([]byte(c.on), &r); err != nil {
			t.Fatalf("%s: %v", c.on, err)
		}
		if string(r) != c.want {
			t.Errorf("%s decoded to %q, want %q", c.on, r, c.want)
		}
	}
}

// Providers speak user/assistant on their own wire; figaro speaks input/output.
// A rename of figaro's vocabulary must never reach a provider payload.
func TestRoleFromWire_IsTheOnlyBoundary(t *testing.T) {
	if got := message.RoleFromWire("user"); got != message.RoleInput {
		t.Errorf("wire user -> %q, want %q", got, message.RoleInput)
	}
	if got := message.RoleFromWire("assistant"); got != message.RoleOutput {
		t.Errorf("wire assistant -> %q, want %q", got, message.RoleOutput)
	}
	if got := message.RoleFromWire("something_else"); got != message.Role("something_else") {
		t.Errorf("unknown role must pass through, got %q", got)
	}
}

func TestToolImagesByCall(t *testing.T) {
	tests := []struct {
		name    string
		content []message.Content
		want    map[string]int
	}{
		{
			name:    "no tool results claims nothing",
			content: []message.Content{message.ImageContent("image/png", "AAAA")},
		},
		{
			name: "an image is claimed by its call",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "ok", false),
				message.ToolImageContent("call-1", "read", "image/png", "AAAA"),
			},
			want: map[string]int{"call-1": 1},
		},
		{
			name: "an untagged image is left for the caller to render",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "ok", false),
				message.ImageContent("image/png", "AAAA"),
			},
		},
		{
			name: "an image naming a call with no result is left unclaimed",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "ok", false),
				message.ToolImageContent("call-9", "ghost", "image/png", "AAAA"),
			},
		},
		{
			name: "an empty image is not claimed",
			content: []message.Content{
				message.ToolResultContent("call-1", "read", "ok", false),
				message.ToolImageContent("call-1", "read", "image/png", ""),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := message.ToolImagesByCall(tt.content)
			assert.Len(t, got, len(tt.want))
			for id, n := range tt.want {
				assert.Len(t, got[id], n)
			}
		})
	}
}
