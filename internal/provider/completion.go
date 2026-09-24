package provider

import (
	"fmt"

	"github.com/jack-work/figaro/api/message"
)

// AnthropicStopReason maps a Messages-API stop_reason onto the IR's. Shared
// by every Anthropic-shaped provider so a word one of them learns the other
// cannot forget. A word this table does not know is StopError, never the
// empty string: an empty stop reason used to decode as "" and a later default
// turned it into a normal stop, which is how a refusal became a blank reply
// (jack-work/figaro#24).
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

// CompletionFault is a completed stream that cannot be taken as a reply:
// the provider stopped for a reason that is not a way of finishing, or it
// finished with nothing to say. Wire is the provider's own word, kept so the
// user reads what the proxy said and not figaro's paraphrase of it.
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

// CheckCompletion is the verdict on a stream that closed cleanly. nil means
// the message is a reply and may land as one. Otherwise the caller decides
// what to do with any content the model produced (keep it, marked StopError)
// and returns the fault so the turn ends in error rather than in silence.
//
// An assistant message with no content is never a reply, whatever the stop
// reason says: a filter that answers 200 with an empty body is the case this
// exists for, and it does not depend on knowing the filter's vocabulary.
func CheckCompletion(providerName string, msg message.Message, wire string) error {
	empty := len(msg.Content) == 0
	if !empty && msg.StopReason != message.StopError {
		return nil
	}
	return &CompletionFault{Provider: providerName, Wire: wire, Empty: empty}
}
