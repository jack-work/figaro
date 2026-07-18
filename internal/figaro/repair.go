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

// appendInterruptSentinelIfDangling inspects the tail of the IR log
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
// Returns the stamped sentinel entry and true when one was appended,
// so a live caller can fan it out as a log entry.
func appendInterruptSentinelIfDangling(stream store.Log[message.Message], ariaID string) (store.Entry[message.Message], bool) {
	tail, ok := stream.PeekTail()
	if !ok || tail.Payload.Role != message.RoleAssistant {
		return store.Entry[message.Message]{}, false
	}

	assistant := tail.Payload
	if assistant.StopReason != message.StopToolInvoke {
		return store.Entry[message.Message]{}, false
	}

	calls := assistantToolInvokes(assistant)
	if len(calls) == 0 {
		return store.Entry[message.Message]{}, false
	}

	sentinel := message.NewInterruptSentinel(
		message.InterruptFault,
		interruptSentinelText,
		calls,
	)
	sentinel.Timestamp = time.Now().UnixMilli()
	stamped, err := stream.Append(store.Entry[message.Message]{Payload: sentinel})
	if err != nil {
		slog.Error("append interrupt sentinel", "aria", ariaID, "err", err)
		return store.Entry[message.Message]{}, false
	}
	slog.Warn("appended interrupt sentinel for dangling tool_use",
		"aria", ariaID,
		"assistant_lt", tail.LT,
		"tool_count", len(calls),
	)
	return stamped, true
}
