package figaro

import (
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

const tailRepairNotice = "process died mid-turn; output not captured"

// repairTailLookback bounds the read: an open call sits within one round of
// the tail, and this runs at every open of every aria.
const repairTailLookback = 64

func repairInterruptedTail(stream store.Log[message.Message], ariaID string) (store.Entry[message.Message], bool) {
	rows := store.TailSnapshot(stream, repairTailLookback)
	at := -1
	for i := len(rows) - 1; i >= 0; i-- {
		if len(assistantToolInvokes(rows[i].Payload)) > 0 {
			at = i
			break
		}
	}
	if at < 0 {
		return store.Entry[message.Message]{}, false
	}
	// A partial answer is left alone: an append-only log cannot splice a result
	// in front of a tic already there. Only a wholly unanswered call can be closed.
	for _, e := range rows[at+1:] {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult {
				return store.Entry[message.Message]{}, false
			}
		}
	}
	var results []message.Content
	for _, call := range assistantToolInvokes(rows[at].Payload) {
		results = append(results, message.ToolResultContent(call.ToolCallID, call.ToolName, tailRepairNotice, true))
	}
	if len(results) == 0 {
		return store.Entry[message.Message]{}, false
	}
	stamped, err := stream.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:      message.RoleInput,
		Content:   results,
		Timestamp: time.Now().UnixMilli(),
	}})
	if err != nil {
		slog.Error("append interrupted tool results", "aria", ariaID, "err", err)
		return store.Entry[message.Message]{}, false
	}
	slog.Warn("repaired dangling tool_use",
		"aria", ariaID,
		"assistant_lt", rows[at].LT,
		"tool_count", len(results),
	)
	return stamped, true
}
