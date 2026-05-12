package figaro

import (
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// interruptSentinelText is the human-readable Text echoed into each
// ContentInterrupt block. Translators surface it inside the synthetic
// wire surrogate so the model sees why the tool result is missing.
const interruptSentinelText = "tool execution was interrupted (recovered on reload)"

// appendInterruptSentinelIfDangling inspects the tail of the IR stream
// and, if it ends on an assistant turn with unmatched tool_use blocks,
// appends a RoleSystemInterrupt sentinel naming the dangling
// tool_call_ids. The IR remains append-only; downstream translators
// map the sentinel to a provider-acceptable surrogate.
//
// Idempotent: a stream whose tail is already a sentinel (or whose
// dangling tool_use is followed by complete tool_results) is left
// unchanged. If the dangling assistant turn is not the last entry,
// no sentinel is inserted (the stream is in an unrecoverable shape
// under append-only semantics; operator-driven repair is needed).
func appendInterruptSentinelIfDangling(stream store.Stream[message.Message], ariaID string) {
	entries := stream.Read()
	if len(entries) == 0 {
		return
	}

	// Find the last assistant turn.
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

	calls := assistantToolCalls(assistant)
	if len(calls) == 0 {
		return
	}

	// Anything between the dangling assistant and the tail?
	trailing := entries[lastAssistantIdx+1:]
	switch {
	case len(trailing) == 0:
		// Dangling tool_use at the tail. Append a sentinel.
	case isResultTicCovering(trailing[0].Payload, calls):
		// Already satisfied by a real tool_result tic.
		return
	case message.IsInterruptSentinel(trailing[0].Payload):
		// Already repaired by a prior boot.
		return
	default:
		// Some other entry sits between the dangling assistant and the
		// tail. Under append-only we cannot splice in front of it; log
		// and leave for an operator to inspect.
		slog.Warn("dangling tool_use followed by unrelated entries; not appending sentinel",
			"aria", ariaID,
			"assistant_lt", entries[lastAssistantIdx].LT,
			"trailing_count", len(trailing),
		)
		return
	}

	sentinel := message.NewInterruptSentinel(
		message.InterruptFault,
		interruptSentinelText,
		calls,
	)
	sentinel.Timestamp = time.Now().UnixMilli()
	if _, err := stream.Append(store.Entry[message.Message]{Payload: sentinel}); err != nil {
		slog.Error("append interrupt sentinel", "aria", ariaID, "err", err)
		return
	}
	slog.Warn("appended interrupt sentinel for dangling tool_use",
		"aria", ariaID,
		"assistant_lt", entries[lastAssistantIdx].LT,
		"tool_count", len(calls),
	)
}

// isResultTicCovering reports whether m is a user-role tic carrying
// tool_result blocks for every call.
func isResultTicCovering(m message.Message, calls []message.Content) bool {
	if m.Role != message.RoleUser {
		return false
	}
	have := make(map[string]bool, len(m.Content))
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			have[c.ToolCallID] = true
		}
	}
	for _, tc := range calls {
		if !have[tc.ToolCallID] {
			return false
		}
	}
	return true
}
