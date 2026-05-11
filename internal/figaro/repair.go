package figaro

import (
	"log/slog"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// repairDanglingToolUse fixes a stream where the last assistant
// message has stop_reason=tool_use but no matching tool_results.
// Happens on interrupt/crash/SIGKILL. Appends synthetic error
// tool_results to make the stream well-formed.
func repairDanglingToolUse(stream store.Stream[message.Message], ariaID string) {
	entries := stream.Read()
	if len(entries) == 0 {
		return
	}


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


	var calls []message.Content
	for _, c := range assistant.Content {
		if c.Type == message.ContentToolCall {
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return
	}


	if lastAssistantIdx+1 < len(entries) {
		next := entries[lastAssistantIdx+1].Payload
		if next.Role == message.RoleUser && hasAllToolResults(next, calls) {
			return // stream is well-formed
		}
	}

	// Truncate trailing non-tool-result messages, append synthetic
	// tool_results, then re-append any orphaned messages.
	trailingAfterAssistant := entries[lastAssistantIdx+1:]

	if len(trailingAfterAssistant) > 0 {
		slog.Warn("repairing dangling tool_use: truncating and re-appending trailing messages",
			"aria", ariaID,
			"assistant_lt", entries[lastAssistantIdx].LT,
			"trailing_count", len(trailingAfterAssistant),
		)

		stream.Truncate(entries[lastAssistantIdx].LT)
	} else {
		slog.Warn("repairing dangling tool_use: appending synthetic tool_results",
			"aria", ariaID,
			"assistant_lt", entries[lastAssistantIdx].LT,
			"tool_count", len(calls),
		)
	}


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


	for _, e := range trailingAfterAssistant {
		if _, err := stream.Append(store.Entry[message.Message]{Payload: e.Payload}); err != nil {
			slog.Error("repair: re-append trailing message", "aria", ariaID, "err", err)
			return
		}
	}
}

// hasAllToolResults checks if a user message has results for all calls.
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
