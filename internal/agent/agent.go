// Package agent implements the event-driven orchestration loop.
//
// All events flow through a single inbox channel (the actor mailbox).
// A single goroutine drains the inbox and processes events:
//
//   - EventUserPrompt    → append to store, start LLM streaming
//   - EventLLMDelta      → emit to subscribers (display only)
//   - EventLLMDone       → append to store, check for tool calls
//   - EventToolStart     → emit to subscribers
//   - EventToolOutput    → emit to subscribers (display only)
//   - EventToolResult    → append to store, emit, maybe send back to LLM
//   - EventError         → emit to subscribers
//
// LLM streaming and tool execution run in background goroutines that
// push events into the inbox. The loop is the single point of emission —
// no goroutine calls emit directly.
package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// --- Event types ---

type eventType int

const (
	eventUserPrompt eventType = iota
	eventLLMDelta
	eventLLMDone
	eventLLMError
	eventToolStart
	eventToolOutput
	eventToolResult
)

type event struct {
	typ eventType

	// eventUserPrompt
	text string

	// eventLLMDelta
	delta       string
	contentType message.ContentType

	// eventLLMDone
	message *message.Message

	// eventLLMError, eventToolResult (when isErr)
	err error

	// eventToolStart, eventToolOutput, eventToolResult
	toolCallID string
	toolName   string
	arguments  map[string]interface{}

	// eventToolOutput
	chunk string

	// eventToolResult
	result string
	isErr  bool
}

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

	// inbox is the actor mailbox. All events flow through here.
	inbox chan event
}

// Prompt appends a user message and runs the event loop to completion.
func (a *Agent) Prompt(ctx context.Context, text string) error {
	a.inbox = make(chan event, 128)

	// Seed the loop with the user prompt event.
	a.inbox <- event{typ: eventUserPrompt, text: text}

	return a.eventLoop(ctx)
}

// eventLoop is the single processing goroutine. It reads events from
// the inbox and handles each one. LLM streaming and tool execution
// push events back into the inbox from background goroutines.
func (a *Agent) eventLoop(ctx context.Context) error {
	// pendingTools tracks how many tool results we're waiting for
	// in the current tool-call batch.
	var pendingTools int
	var pendingToolCalls []message.Content

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case evt := <-a.inbox:
			switch evt.typ {

			case eventUserPrompt:
				fmt.Fprintf(os.Stderr, "agent: event=UserPrompt text=%q\n", truncLog(evt.text, 60))
				userMsg := message.Message{
					Role:      message.RoleUser,
					Content:   []message.Content{message.TextContent(evt.text)},
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
				// Start LLM streaming.
				a.startLLMStream(ctx)

			case eventLLMDelta:
				a.emit(rpc.MethodDelta, rpc.DeltaParams{
					Text: evt.delta, ContentType: evt.contentType,
				})

			case eventLLMDone:
				fmt.Fprintf(os.Stderr, "agent: event=LLMDone stop_reason=%s\n", evt.message.StopReason)
				if evt.message == nil {
					return fmt.Errorf("no response from provider")
				}
				// Append assistant message to store.
				lt, err := a.Store.Append(*evt.message)
				if err != nil {
					return fmt.Errorf("append assistant message: %w", err)
				}
				evt.message.LogicalTime = lt
				a.emit(rpc.MethodMessage, rpc.MessageParams{
					LogicalTime: lt, Message: *evt.message,
				})

				// Check for tool calls.
				var toolCalls []message.Content
				for _, c := range evt.message.Content {
					if c.Type == message.ContentToolCall {
						toolCalls = append(toolCalls, c)
					}
				}

				if len(toolCalls) == 0 {
					// No tool calls — turn is complete.
					a.emit(rpc.MethodDone, rpc.DoneParams{
						Reason: string(evt.message.StopReason),
					})
					return nil
				}

				// Execute tools sequentially: start the first one,
				// queue the rest.
				pendingToolCalls = toolCalls[1:]
				pendingTools = len(toolCalls)
				tc := toolCalls[0]
				a.emit(rpc.MethodToolStart, rpc.ToolStartParams{
					ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
					Arguments: tc.Arguments,
				})
				go a.runToolAsync(ctx, tc)

			case eventLLMError:
				fmt.Fprintf(os.Stderr, "agent: event=LLMError err=%v\n", evt.err)
				a.emit(rpc.MethodError, rpc.ErrorParams{Message: evt.err.Error()})
				return fmt.Errorf("provider stream: %w", evt.err)

			case eventToolOutput:
				a.emit(rpc.MethodToolOutput, rpc.ToolOutputParams{
					ToolCallID: evt.toolCallID,
					ToolName:   evt.toolName,
					Chunk:      evt.chunk,
				})

			case eventToolResult:
				fmt.Fprintf(os.Stderr, "agent: event=ToolResult tool=%s err=%v result_len=%d\n",
					evt.toolName, evt.isErr, len(evt.result))
				a.emit(rpc.MethodToolEnd, rpc.ToolEndParams{
					ToolCallID: evt.toolCallID, ToolName: evt.toolName,
					Result: evt.result, IsError: evt.isErr,
				})

				// Append tool result to store.
				resultMsg := message.NewToolResult(
					evt.toolCallID, evt.toolName,
					[]message.Content{message.TextContent(evt.result)},
					evt.isErr, 0, time.Now().UnixMilli(),
				)
				lt, err := a.Store.Append(resultMsg)
				if err != nil {
					return fmt.Errorf("append tool result: %w", err)
				}
				resultMsg.LogicalTime = lt
				a.emit(rpc.MethodMessage, rpc.MessageParams{
					LogicalTime: lt, Message: resultMsg,
				})

				pendingTools--

				if len(pendingToolCalls) > 0 {
					// Start the next tool in the batch.
					tc := pendingToolCalls[0]
					pendingToolCalls = pendingToolCalls[1:]
					a.emit(rpc.MethodToolStart, rpc.ToolStartParams{
						ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
						Arguments: tc.Arguments,
					})
					go a.runToolAsync(ctx, tc)
				} else if pendingTools == 0 {
					// All tools done — send results back to LLM.
					a.startLLMStream(ctx)
				}
			}
		}
	}
}

// startLLMStream sends the current context to the LLM in a background
// goroutine. Events are pushed back into the inbox.
func (a *Agent) startLLMStream(ctx context.Context) {
	block := a.Store.Context()
	if block == nil {
		a.send(ctx, event{typ: eventLLMError, err: fmt.Errorf("empty context")})
		return
	}

	// Inject system prompt.
	if block.Header == nil && a.SystemPrompt != "" {
		block.Header = &message.Message{
			Role:    message.RoleSystem,
			Content: []message.Content{message.TextContent(a.SystemPrompt)},
		}
	}

	fmt.Fprintf(os.Stderr, "agent: starting LLM stream, %d messages in context\n", len(block.Messages))
	ch, err := a.Provider.Send(ctx, block, a.toolDefs(), a.MaxTokens)
	if err != nil {
		a.send(ctx, event{typ: eventLLMError, err: fmt.Errorf("provider send: %w", err)})
		return
	}

	go func() {
		for evt := range ch {
			if evt.Delta != "" {
				if !a.send(ctx, event{
					typ: eventLLMDelta, delta: evt.Delta,
					contentType: evt.ContentType,
				}) {
					return
				}
			}
			if evt.Done {
				if evt.Err != nil {
					a.send(ctx, event{typ: eventLLMError, err: evt.Err})
				} else {
					a.send(ctx, event{typ: eventLLMDone, message: evt.Message})
				}
				return
			}
		}
		a.send(ctx, event{typ: eventLLMError, err: fmt.Errorf("stream ended unexpectedly")})
	}()
}

// runToolAsync executes a tool in a background goroutine and pushes
// events into the inbox.
func (a *Agent) runToolAsync(ctx context.Context, tc message.Content) {
	var found bool
	for _, t := range a.Tools {
		if t.Name() == tc.ToolName {
			found = true
			onOutput := func(chunk []byte) {
				a.send(ctx, event{
					typ:        eventToolOutput,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					chunk:      string(chunk),
				})
			}
			result, err := t.Execute(ctx, tc.Arguments, onOutput)
			if err != nil {
				a.send(ctx, event{
					typ:        eventToolResult,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					result:     fmt.Sprintf("Error: %s", err),
					isErr:      true,
				})
			} else {
				a.send(ctx, event{
					typ:        eventToolResult,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					result:     result,
				})
			}
			return
		}
	}
	if !found {
		a.send(ctx, event{
			typ:        eventToolResult,
			toolCallID: tc.ToolCallID,
			toolName:   tc.ToolName,
			result:     fmt.Sprintf("Unknown tool: %s", tc.ToolName),
			isErr:      true,
		})
	}
}

// send pushes an event into the inbox, or returns false if the context
// is cancelled. Every goroutine that pushes into the inbox must use this
// instead of a bare channel send to avoid goroutine leaks.
func (a *Agent) send(ctx context.Context, evt event) bool {
	select {
	case a.inbox <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}

func truncLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
