// Package agent implements the core agentic loop.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/session"
	"github.com/jack-work/figaro/internal/tool"
)

type Agent struct {
	Provider     provider.Provider
	Session      *session.Session
	Tools        []tool.Tool
	SystemPrompt string
	MaxTokens    int
	OnDelta      func(delta string, contentType message.ContentType)
}

func (a *Agent) Prompt(ctx context.Context, text string) error {
	userMsg := message.Message{
		Role:      message.RoleUser,
		Content:   []message.Content{message.TextContent(text)},
		Timestamp: time.Now().UnixMilli(),
	}
	a.Session.Append(userMsg)
	return a.runLoop(ctx)
}

func (a *Agent) runLoop(ctx context.Context) error {
	for {
		msgs := a.Session.BuildContext()
		req := provider.Request{
			SystemPrompt: a.SystemPrompt, Messages: msgs,
			Tools: a.toolDefs(), MaxTokens: a.MaxTokens,
		}

		ch, err := a.Provider.Stream(ctx, req)
		if err != nil {
			return fmt.Errorf("stream: %w", err)
		}

		var assistantMsg *message.Message
		for evt := range ch {
			if evt.Delta != "" && a.OnDelta != nil {
				a.OnDelta(evt.Delta, evt.ContentType)
			}
			if evt.Done {
				if evt.Err != nil {
					return fmt.Errorf("stream error: %w", evt.Err)
				}
				assistantMsg = evt.Message
			}
		}
		if assistantMsg == nil {
			return fmt.Errorf("no response from provider")
		}
		a.Session.Append(*assistantMsg)

		var toolCalls []message.Content
		for _, c := range assistantMsg.Content {
			if c.Type == message.ContentToolCall {
				toolCalls = append(toolCalls, c)
			}
		}
		if len(toolCalls) == 0 {
			return nil
		}

		for _, tc := range toolCalls {
			result, isErr := a.executeTool(ctx, tc)
			resultMsg := message.ToolResultMessage(
				tc.ToolCallID, tc.ToolName,
				[]message.Content{message.TextContent(result)},
				isErr, time.Now().UnixMilli(),
			)
			a.Session.Append(resultMsg)
		}
	}
}

func (a *Agent) executeTool(ctx context.Context, tc message.Content) (string, bool) {
	for _, t := range a.Tools {
		if t.Name() == tc.ToolName {
			result, err := t.Execute(ctx, tc.Arguments)
			if err != nil {
				return fmt.Sprintf("Error: %s", err), true
			}
			return result, false
		}
	}
	return fmt.Sprintf("Unknown tool: %s", tc.ToolName), true
}

func (a *Agent) toolDefs() []provider.Tool {
	defs := make([]provider.Tool, len(a.Tools))
	for i, t := range a.Tools {
		defs[i] = provider.Tool{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()}
	}
	return defs
}
