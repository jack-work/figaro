package provider

import (
	"fmt"

	"github.com/jack-work/figaro/api/message"
)

// AnthropicStopReason maps a Messages-API stop_reason onto the IR's. An
// unknown word is StopError, never the empty string.
func AnthropicStopReason(raw string) message.StopReason {
	switch raw {
	case "end_turn", "stop", "stop_sequence":
		return message.StopEnd
	case "max_tokens", "length":
		return message.StopLength
	case "tool_use":
		return message.StopToolInvoke
	case string(message.StopAborted):
		return message.StopAborted
	}
	return message.StopError
}

// CompletionFault is a cleanly closed stream that is not a reply; Wire is
// the provider's own stop reason.
type CompletionFault struct {
	Provider string
	Wire     string
	Empty    bool
}

func (f *CompletionFault) Error() string {
	switch {
	case f.Wire == "":
		return f.Provider + ": the stream closed with no stop reason"
	case f.Wire == "refusal" || f.Wire == "content_filter":
		return fmt.Sprintf("%s: the provider refused the completion (stop_reason=%s): the conversation likely contains content the upstream filter rejected", f.Provider, f.Wire)
	case f.Empty:
		return fmt.Sprintf("%s: the provider returned an empty completion (stop_reason=%s)", f.Provider, f.Wire)
	}
	return fmt.Sprintf("%s: the provider stopped for a reason figaro does not know (stop_reason=%s)", f.Provider, f.Wire)
}

// CheckCompletion returns nil when the message may land as a reply. A message
// with no content is never one, whatever the stop reason says.
func CheckCompletion(providerName string, msg message.Message, wire string) error {
	empty := len(msg.Content) == 0
	if !empty && msg.StopReason != message.StopError {
		return nil
	}
	return &CompletionFault{Provider: providerName, Wire: wire, Empty: empty}
}
