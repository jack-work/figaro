package figaro

import (
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

const tailRepairNotice = "process died mid-turn; output not captured"

func repairInterruptedTail(stream store.Log[message.Message], ariaID string) (store.Entry[message.Message], bool) {
	tail, ok := stream.PeekTail()
	if !ok || tail.Payload.Role != message.RoleAssistant {
		return store.Entry[message.Message]{}, false
	}
	calls := assistantToolInvokes(tail.Payload)
	if len(calls) == 0 {
		return store.Entry[message.Message]{}, false
	}
	results := make([]message.Content, 0, len(calls))
	for _, call := range calls {
		results = append(results, message.ToolResultContent(call.ToolCallID, call.ToolName, tailRepairNotice, true))
	}
	stamped, err := stream.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleUser,
		Content:   results,
		Timestamp: time.Now().UnixMilli(),
	}})
	if err != nil {
		slog.Error("append interrupted tool results", "aria", ariaID, "err", err)
		return store.Entry[message.Message]{}, false
	}
	slog.Warn("repaired dangling tool_use tail",
		"aria", ariaID,
		"assistant_lt", tail.LT,
		"tool_count", len(calls),
	)
	return stamped, true
}
