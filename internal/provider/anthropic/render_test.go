package anthropic

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
)

func TestRenderTag_AppendsBlocksToLastUserMessage(t *testing.T) {
	req := &nativeRequest{
		Messages: []nativeMessage{
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "first"}}},
			{Role: "assistant", Content: []nativeBlock{{Type: "text", Text: "reply"}}},
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "current prompt"}}},
		},
	}
	reminders := []chalkboard.RenderedEntry{
		{Key: "cwd", Body: "Working directory: /foo"},
		{Key: "model", Body: "Model: claude-opus"},
	}

	renderTag(req, reminders)

	last := req.Messages[len(req.Messages)-1]
	require.Equal(t, "user", last.Role)
	require.Len(t, last.Content, 3, "original text + 2 reminder blocks")
	assert.Equal(t, "current prompt", last.Content[0].Text)
	assert.Contains(t, last.Content[1].Text, `<system-reminder name="cwd">`)
	assert.Contains(t, last.Content[1].Text, "Working directory: /foo")
	assert.Contains(t, last.Content[1].Text, `</system-reminder>`)
	assert.Contains(t, last.Content[2].Text, `<system-reminder name="model">`)
}

func TestRenderTag_EmptyReminders_NoOp(t *testing.T) {
	req := &nativeRequest{
		Messages: []nativeMessage{
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "hello"}}},
		},
	}
	(&Anthropic{}).applyRenderer(req, nil)
	require.Len(t, req.Messages, 1)
	assert.Len(t, req.Messages[0].Content, 1, "no reminders → no mutation")
}

func TestRenderTool_AppendsAssistantPlusUserPair(t *testing.T) {
	req := &nativeRequest{
		Messages: []nativeMessage{
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "current prompt"}}},
		},
	}
	reminders := []chalkboard.RenderedEntry{
		{Key: "cwd", Body: "Working directory: /foo"},
		{Key: "model", Body: "Model: claude-opus"},
	}

	renderTool(req, reminders)

	require.Len(t, req.Messages, 3, "original + synthetic assistant + synthetic user")

	assistant := req.Messages[1]
	require.Equal(t, "assistant", assistant.Role)
	require.Len(t, assistant.Content, 2, "one tool_use per reminder")
	assert.Equal(t, "tool_use", assistant.Content[0].Type)
	assert.Equal(t, "cwd", assistant.Content[0].Name)
	assert.Equal(t, "harness-notice-0", assistant.Content[0].ID)
	assert.Equal(t, "tool_use", assistant.Content[1].Type)
	assert.Equal(t, "model", assistant.Content[1].Name)
	assert.Equal(t, "harness-notice-1", assistant.Content[1].ID)

	user := req.Messages[2]
	require.Equal(t, "user", user.Role)
	require.Len(t, user.Content, 2, "one tool_result per reminder")
	assert.Equal(t, "tool_result", user.Content[0].Type)
	assert.Equal(t, "harness-notice-0", user.Content[0].ToolUseID)
	assert.Equal(t, "tool_result", user.Content[1].Type)
	assert.Equal(t, "harness-notice-1", user.Content[1].ToolUseID)
}

func TestRenderTool_DoesNotDeclareSyntheticTool(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tool"}
	req := &nativeRequest{
		Tools: []nativeTool{
			{Name: "bash", Description: "shell", InputSchema: map[string]interface{}{}},
		},
		Messages: []nativeMessage{
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "hello"}}},
		},
	}
	a.applyRenderer(req, []chalkboard.RenderedEntry{
		{Key: "harness_notice", Body: "extra context"},
	})

	// Tools should be unchanged — the synthetic tool name does NOT
	// appear in req.Tools. The model can't call it going forward.
	require.Len(t, req.Tools, 1)
	assert.Equal(t, "bash", req.Tools[0].Name)
	for _, tool := range req.Tools {
		assert.NotContains(t, []string{"harness_notice", "harness-notice"}, tool.Name,
			"synthetic tool must not be declared in req.Tools")
	}
}

func TestApplyRenderer_DefaultIsTag(t *testing.T) {
	a := &Anthropic{} // no ReminderRenderer set → default
	req := &nativeRequest{
		Messages: []nativeMessage{
			{Role: "user", Content: []nativeBlock{{Type: "text", Text: "current"}}},
		},
	}
	a.applyRenderer(req, []chalkboard.RenderedEntry{{Key: "k", Body: "v"}})

	last := req.Messages[len(req.Messages)-1]
	require.Equal(t, "user", last.Role, "default renderer should keep last message as user")
	require.GreaterOrEqual(t, len(last.Content), 2, "tag renderer adds content blocks to last user")
	assert.True(t, strings.Contains(last.Content[1].Text, "<system-reminder"))
}

func TestApplyRenderer_PrefixUntouched_TagAndTool(t *testing.T) {
	// The cache prefix invariant: renderers must NEVER mutate req.System,
	// req.Tools, or any message before the leaf user message. We construct
	// a request, snapshot its prefix bytes, run each renderer, and assert
	// the prefix is byte-identical after.
	makeReq := func() *nativeRequest {
		return &nativeRequest{
			System: []systemBlock{{Type: "text", Text: "credo"}},
			Tools: []nativeTool{
				{Name: "bash", Description: "shell", InputSchema: map[string]interface{}{}},
			},
			Messages: []nativeMessage{
				{Role: "user", Content: []nativeBlock{{Type: "text", Text: "msg-0"}}},
				{Role: "assistant", Content: []nativeBlock{{Type: "text", Text: "msg-1"}}},
				{Role: "user", Content: []nativeBlock{{Type: "text", Text: "leaf"}}},
			},
		}
	}
	reminders := []chalkboard.RenderedEntry{{Key: "cwd", Body: "Working directory: /foo"}}

	for _, renderer := range []string{"tag", "tool"} {
		t.Run(renderer, func(t *testing.T) {
			req := makeReq()
			a := &Anthropic{ReminderRenderer: renderer}

			// Capture prefix.
			origSystem := req.System[0].Text
			origTools := req.Tools[0].Name
			origMsg0 := req.Messages[0].Content[0].Text
			origMsg1 := req.Messages[1].Content[0].Text

			a.applyRenderer(req, reminders)

			assert.Equal(t, origSystem, req.System[0].Text, "system must not change")
			assert.Equal(t, origTools, req.Tools[0].Name, "tools must not change")
			assert.Equal(t, origMsg0, req.Messages[0].Content[0].Text, "message[0] must not change")
			assert.Equal(t, origMsg1, req.Messages[1].Content[0].Text, "message[1] must not change")
		})
	}
}
