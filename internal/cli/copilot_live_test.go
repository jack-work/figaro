package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type copilotLiveBus struct {
	messages  []message.Message
	toolReady []message.Content
}

func (b *copilotLiveBus) PushDelta(message.Content) {}

func (b *copilotLiveBus) PushFigaro(msg message.Message, _ ...provider.AssistantCache) {
	b.messages = append(b.messages, msg)
}

func (b *copilotLiveBus) PushToolInvokeStart(string, string) {}

func (b *copilotLiveBus) PushToolInvokeDelta(string, string) {}

func (b *copilotLiveBus) PushToolReady(call message.Content) {
	b.toolReady = append(b.toolReady, call)
}

func (b *copilotLiveBus) PushMessageEnd(string) {}

func TestLiveCopilotResponses(t *testing.T) {
	if os.Getenv("FIGARO_LIVE_COPILOT_TEST") != "1" {
		t.Skip("set FIGARO_LIVE_COPILOT_TEST=1 to run against the local Copilot credential")
	}

	loaded := mustLoadConfig()
	p, _ := buildProvider(loaded, "copilot")
	require.NotNil(t, p, "configure Copilot before running the live test")
	p.SetModel("gpt-5.6-terra")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	models, err := p.Models(ctx)
	require.NoError(t, err)
	var found bool
	for _, model := range models {
		if model.ID == "gpt-5.6-terra" {
			found = true
			break
		}
	}
	require.True(t, found, "gpt-5.6-terra is not available to the current Copilot account")

	log := store.NewMemLog[message.Message]()
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("Reply with exactly: FIGARO_COPILOT_SMOKE")},
	}})
	require.NoError(t, err)

	bus := &copilotLiveBus{}
	require.NoError(t, p.Send(ctx, provider.SendInput{
		AriaID: "live-copilot-smoke",
		FigLog: log,
	}, bus))
	require.Len(t, bus.messages, 1)
	require.NotEmpty(t, bus.messages[0].Content)
	require.Equal(t, "FIGARO_COPILOT_SMOKE", strings.TrimSpace(bus.messages[0].Content[0].Text))
}

func TestLiveCopilotResponsesToolRoundTrip(t *testing.T) {
	if os.Getenv("FIGARO_LIVE_COPILOT_TEST") != "1" {
		t.Skip("set FIGARO_LIVE_COPILOT_TEST=1 to run against the local Copilot credential")
	}

	loaded := mustLoadConfig()
	p, _ := buildProvider(loaded, "copilot")
	require.NotNil(t, p, "configure Copilot before running the live test")
	p.SetModel("gpt-5.6-terra")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := store.NewMemLog[message.Message]()
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser,
		Content: []message.Content{message.TextContent(
			`Call figaro_echo exactly once with value "FIGARO_TOOL_SMOKE". Do not answer before calling it. After receiving the tool result, reply with exactly: FIGARO_TOOL_DONE.`,
		)},
	}})
	require.NoError(t, err)

	toolDef := provider.Tool{
		Name:        "figaro_echo",
		Description: "Returns the supplied value.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []string{"value"},
		},
	}
	first := &copilotLiveBus{}
	require.NoError(t, p.Send(ctx, provider.SendInput{
		AriaID: "live-copilot-tool-smoke",
		FigLog: log,
		Tools:  []provider.Tool{toolDef},
	}, first))
	require.Len(t, first.messages, 1)
	require.Len(t, first.toolReady, 1)
	call := first.toolReady[0]
	require.Equal(t, "figaro_echo", call.ToolName)
	require.Equal(t, "FIGARO_TOOL_SMOKE", call.Arguments["value"])
	require.Equal(t, message.StopToolInvoke, first.messages[0].StopReason)

	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser,
		Content: []message.Content{
			message.ToolResultContent(call.ToolCallID, call.ToolName, "FIGARO_TOOL_RESULT", false),
		},
	}})
	require.NoError(t, err)

	second := &copilotLiveBus{}
	require.NoError(t, p.Send(ctx, provider.SendInput{
		AriaID: "live-copilot-tool-smoke",
		FigLog: log,
		Tools:  []provider.Tool{toolDef},
	}, second))
	require.Len(t, second.messages, 1)
	require.NotEmpty(t, second.messages[0].Content)
	require.Equal(t, message.StopEnd, second.messages[0].StopReason)
	require.Equal(t, "FIGARO_TOOL_DONE", strings.TrimSpace(second.messages[0].Content[0].Text))
}

func TestLiveCopilotResponsesLongContext(t *testing.T) {
	if os.Getenv("FIGARO_LIVE_COPILOT_TEST") != "1" {
		t.Skip("set FIGARO_LIVE_COPILOT_TEST=1 to run against the local Copilot credential")
	}

	loaded := mustLoadConfig()
	p, _ := buildProvider(loaded, "copilot")
	require.NotNil(t, p, "configure Copilot before running the live test")
	p.SetModel("gpt-5.6-terra")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := store.NewMemLog[message.Message]()
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("Reply with exactly: FIGARO_LONG_CONTEXT_SMOKE")},
	}})
	require.NoError(t, err)

	bus := &copilotLiveBus{}
	require.NoError(t, p.Send(ctx, provider.SendInput{
		AriaID: "live-copilot-long-context",
		FigLog: log,
		Snapshot: map[string]json.RawMessage{
			"system.context_tier":      json.RawMessage(`"long_context"`),
			"system.reasoning_context": json.RawMessage(`"all_turns"`),
			"system.reasoning_summary": json.RawMessage(`"detailed"`),
			"system.thinking_effort":   json.RawMessage(`"low"`),
			"system.verbosity":         json.RawMessage(`"low"`),
		},
	}, bus))
	require.Len(t, bus.messages, 1)
	require.NotEmpty(t, bus.messages[0].Content)
	require.Equal(t, "FIGARO_LONG_CONTEXT_SMOKE", strings.TrimSpace(bus.messages[0].Content[0].Text))
}
