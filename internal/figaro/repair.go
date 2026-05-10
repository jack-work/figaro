package figaro

import (
	"log/slog"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// repairDanglingToolUse checks the tail of the figaro IR stream for
// structural integrity and repairs it if needed. The Anthropic API
// requires every assistant message with stop_reason=tool_use to be
// followed by a user message containing matching tool_result blocks.
//
// A dangling tool_use can occur when:
//   - The user interrupts (Ctrl+C) while tools are running and the
//     agent exits before the synthetic tool_result tic is appended.
//   - The agent crashes or is SIGKILLed mid-tool.
//   - A bug in the turn loop.
//
// Detection: walk backward from the tail. If the last assistant
// message has stop_reason=tool_use and the message after it (if any)
// is not a user message with tool_result blocks, the stream is broken.
//
// Repair strategy: append a synthetic user message with error
// tool_result blocks for each dangling tool_call. This is the
// smallest repair that makes the stream well-formed — the model
// sees "tools were interrupted" and can choose to retry or move on.
func repairDanglingToolUse(stream store.Stream[message.Message], ariaID string) {
	entries := stream.Read()
	if len(entries) == 0 {
		return
	}

	// Find the last assistant message.
	lastAssistantIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Payload.Role == message.RoleAssistant {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx < 0 {
		return
	}

	assistant := entries[lastAssistantIdx].Payload
	if assistant.StopReason != message.StopToolUse {
		return
	}

	// Collect the tool_call IDs from this assistant message.
	var calls []message.Content
	for _, c := range assistant.Content {
		if c.Type == message.ContentToolCall {
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return
	}

	// Check if the next message is a user message with matching tool_results.
	if lastAssistantIdx+1 < len(entries) {
		next := entries[lastAssistantIdx+1].Payload
		if next.Role == message.RoleUser && hasAllToolResults(next, calls) {
			return // stream is well-formed
		}
	}

	// Stream is broken. Determine what's trailing after the assistant.
	// If there are user messages after the dangling assistant that are
	// NOT tool results (plain text prompts), those were written into a
	// broken stream. We need to:
	//   1. Truncate everything after the assistant message
	//   2. Append synthetic tool_results
	//   3. Re-append the orphaned user messages
	trailingAfterAssistant := entries[lastAssistantIdx+1:]

	if len(trailingAfterAssistant) > 0 {
		slog.Warn("repairing dangling tool_use: truncating and re-appending trailing messages",
			"aria", ariaID,
			"assistant_lt", entries[lastAssistantIdx].LT,
			"trailing_count", len(trailingAfterAssistant),
		)
		// Truncate to just after the assistant.
		stream.Truncate(entries[lastAssistantIdx].LT)
	} else {
		slog.Warn("repairing dangling tool_use: appending synthetic tool_results",
			"aria", ariaID,
			"assistant_lt", entries[lastAssistantIdx].LT,
			"tool_count", len(calls),
		)
	}

	// Synthesize error tool_results.
	var results []message.Content
	for _, tc := range calls {
		results = append(results, message.ToolResultContent(
			tc.ToolCallID, tc.ToolName,
			"error: tool execution was interrupted (recovered on reload)",
			true,
		))
	}
	resultTic := message.Message{
		Role:    message.RoleUser,
		Content: results,
	}
	if _, err := stream.Append(store.Entry[message.Message]{Payload: resultTic}); err != nil {
		slog.Error("repair: append synthetic tool_result", "aria", ariaID, "err", err)
		return
	}

	// Re-append any orphaned trailing messages.
	for _, e := range trailingAfterAssistant {
		if _, err := stream.Append(store.Entry[message.Message]{Payload: e.Payload}); err != nil {
			slog.Error("repair: re-append trailing message", "aria", ariaID, "err", err)
			return
		}
	}
}

// hasAllToolResults checks whether a user message contains tool_result
// blocks matching every tool_call in calls.
func hasAllToolResults(m message.Message, calls []message.Content) bool {
	resultIDs := make(map[string]bool)
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			resultIDs[c.ToolCallID] = true
		}
	}
	for _, tc := range calls {
		if !resultIDs[tc.ToolCallID] {
			return false
		}
	}
	return true
}
