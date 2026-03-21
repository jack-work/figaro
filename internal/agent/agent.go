// Package agent implements the tic-based orchestration loop.
//
// The loop is synchronous. Each tic:
//  1. store.Context() → get the conversation block
//  2. Inspect the last message
//  3. Act according to message type:
//     - user or tool_result → send to LLM provider
//     - assistant with tool calls → execute tools
//     - assistant with stop → done, yield to caller
//  4. store.Append() each result, one at a time
//
// In parallel with every append, the message is streamed as
// a JSON-RPC 2.0 notification to the output channel.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// Agent orchestrates the conversation loop.
type Agent struct {
	Store        store.Store
	Provider     provider.Provider
	Tools        []tool.Tool
	SystemPrompt string
	MaxTokens    int

	// Out receives JSON-RPC notifications for the frontend.
	// The caller is responsible for reading and rendering.
	Out chan<- rpc.Notification
}

// Prompt appends a user message and runs the tic loop to completion.
func (a *Agent) Prompt(ctx context.Context, text string) error {
	userMsg := message.Message{
		Role:      message.RoleUser,
		Content:   []message.Content{message.TextContent(text)},
		Timestamp: time.Now().UnixMilli(),
	}

	lt, err := a.Store.Append(userMsg)
	if err != nil {
		return fmt.Errorf("append user message: %w", err)
	}
	userMsg.LogicalTime = lt
	a.emit(rpc.MethodMessage, rpc.MessageParams{
		LogicalTime: lt, Message: userMsg,
	})

	return a.ticLoop(ctx)
}

// ticLoop runs until the assistant stops or an error occurs.
func (a *Agent) ticLoop(ctx context.Context) error {
	for {
		block := a.Store.Context()
		if block == nil || len(block.Messages) == 0 {
			return fmt.Errorf("empty context")
		}

		last := block.Messages[len(block.Messages)-1]

		switch last.Role {
		case message.RoleUser, message.RoleToolResult:
			// Send to LLM, stream response, append
			if err := a.sendToProvider(ctx, block); err != nil {
				return err
			}
			// Next tic: context now has the assistant message

		case message.RoleAssistant:
			// Check for tool calls
			var toolCalls []message.Content
			for _, c := range last.Content {
				if c.Type == message.ContentToolCall {
					toolCalls = append(toolCalls, c)
				}
			}

			if len(toolCalls) == 0 {
				// No tool calls — done
				a.emit(rpc.MethodDone, rpc.DoneParams{
					Reason: string(last.StopReason),
				})
				return nil
			}

			// Execute tools, append results one by one
			for _, tc := range toolCalls {
				if err := a.executeTool(ctx, tc); err != nil {
					return err
				}
			}
			// Next tic: context now has tool results

		default:
			return fmt.Errorf("unexpected message role at leaf: %s", last.Role)
		}
	}
}

// sendToProvider streams the block to the LLM and appends the response.
func (a *Agent) sendToProvider(ctx context.Context, block *message.Block) error {
	// Inject system prompt into block header if not already set
	if block.Header == nil && a.SystemPrompt != "" {
		block.Header = &message.Message{
			Role:    message.RoleSystem,
			Content: []message.Content{message.TextContent(a.SystemPrompt)},
		}
	}

	ch, err := a.Provider.Send(ctx, block, a.toolDefs(), a.MaxTokens)
	if err != nil {
		return fmt.Errorf("provider send: %w", err)
	}

	var assistantMsg *message.Message
	for evt := range ch {
		// Stream deltas to frontend
		if evt.Delta != "" {
			a.emit(rpc.MethodDelta, rpc.DeltaParams{
				Text: evt.Delta, ContentType: evt.ContentType,
			})
		}

		if evt.Done {
			if evt.Err != nil {
				a.emit(rpc.MethodError, rpc.ErrorParams{Message: evt.Err.Error()})
				return fmt.Errorf("provider stream: %w", evt.Err)
			}
			assistantMsg = evt.Message
		}
	}

	if assistantMsg == nil {
		return fmt.Errorf("no response from provider")
	}

	lt, err := a.Store.Append(*assistantMsg)
	if err != nil {
		return fmt.Errorf("append assistant message: %w", err)
	}
	assistantMsg.LogicalTime = lt
	a.emit(rpc.MethodMessage, rpc.MessageParams{
		LogicalTime: lt, Message: *assistantMsg,
	})

	return nil
}

// executeTool runs a single tool call and appends the result.
func (a *Agent) executeTool(ctx context.Context, tc message.Content) error {
	a.emit(rpc.MethodToolStart, rpc.ToolStartParams{
		ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
		Arguments: tc.Arguments,
	})

	result, isErr := a.runTool(ctx, tc)

	a.emit(rpc.MethodToolEnd, rpc.ToolEndParams{
		ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
		Result: result, IsError: isErr,
	})

	resultMsg := message.NewToolResult(
		tc.ToolCallID, tc.ToolName,
		[]message.Content{message.TextContent(result)},
		isErr, 0, time.Now().UnixMilli(),
	)

	lt, err := a.Store.Append(resultMsg)
	if err != nil {
		return fmt.Errorf("append tool result: %w", err)
	}
	resultMsg.LogicalTime = lt
	a.emit(rpc.MethodMessage, rpc.MessageParams{
		LogicalTime: lt, Message: resultMsg,
	})

	return nil
}

func (a *Agent) runTool(ctx context.Context, tc message.Content) (string, bool) {
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

func (a *Agent) emit(method string, params interface{}) {
	if a.Out == nil {
		return
	}
	a.Out <- rpc.Notification{
		JSONRPC: "2.0", Method: method, Params: params,
	}
}
