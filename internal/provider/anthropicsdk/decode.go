package anthropicsdk

import (
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/message"
)

// validAccumulatedBlock reports whether an accumulated block is
// API-legal to replay. Shared by the IR decoder (so the sealed message
// matches the in-flight asm, which never creates a node for empty
// text/thinking — keeping them shifts later block indices and
// duplicates the live render; empty summarized-thinking blocks are the
// common case with Display: Summarized) and the cache path (so an
// open+close-with-no-deltas block never persists without its required
// field).
func validAccumulatedBlock(b anthropic.ContentBlockUnion) bool {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != ""
	case "thinking":
		return strings.TrimSpace(b.Thinking) != ""
	case "redacted_thinking":
		return b.Data != ""
	case "tool_use":
		return b.ID != "" && len(b.Input) > 0
	case "":
		return false
	}
	return true
}

// cacheableAccumulatedBlock is the wire-replay predicate — wider than
// validAccumulatedBlock for thinking: a signed empty-summary block must
// replay (the API requires the thinking block leading a tool-use
// assistant) even though the renderer skips it. fatal marks the whole
// message uncacheable: a tool_use whose input is not a JSON object
// cannot replay, and dropping just that block would orphan its
// tool_result on the wire.
func cacheableAccumulatedBlock(b anthropic.ContentBlockUnion) (keep, fatal bool) {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != "", false
	case "thinking":
		return b.Signature != "" || strings.TrimSpace(b.Thinking) != "", false
	case "redacted_thinking":
		return b.Data != "", false
	case "tool_use":
		if b.ID == "" {
			return false, false
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(b.Input, &obj) != nil {
			return false, true
		}
		return true, false
	case "":
		return false, false
	}
	return true, false
}

// decodeAssistantMessage projects an SDK Message (the final
// accumulated assistant turn) to the figaro IR.
func decodeAssistantMessage(m anthropic.Message) message.Message {
	// model/provider are not on the IR message — they live in the
	// chalkboard (system.model / system.provider), derived on read.
	out := message.Message{
		Role: message.RoleAssistant,
	}
	for _, b := range m.Content {
		if !validAccumulatedBlock(b) {
			continue
		}
		switch v := b.AsAny().(type) {
		case anthropic.TextBlock:
			out.Content = append(out.Content, message.Content{Type: message.ContentProse, Text: v.Text})
		case anthropic.ThinkingBlock:
			// Text only — for display and other providers. The signature lives
			// in the cached wire bytes (acc.ToParam), never the IR.
			out.Content = append(out.Content, message.Content{Type: message.ContentThinking, Text: v.Thinking})
		case anthropic.ToolUseBlock:
			out.Content = append(out.Content, message.Content{
				Type:       message.ContentToolInvoke,
				ToolCallID: v.ID,
				ToolName:   v.Name,
				Arguments:  asArgsMap(v.Input),
			})
		}
	}
	out.StopReason = mapStopReason(m.StopReason)
	out.Usage = &message.Usage{
		InputTokens:      int(m.Usage.InputTokens),
		OutputTokens:     int(m.Usage.OutputTokens),
		CacheReadTokens:  int(m.Usage.CacheReadInputTokens),
		CacheWriteTokens: int(m.Usage.CacheCreationInputTokens),
	}
	return out
}

// asArgsMap converts a tool_use Input (json.RawMessage) to a Go map.
func asArgsMap(input json.RawMessage) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	return m
}

func mapStopReason(s anthropic.StopReason) message.StopReason {
	switch s {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return message.StopEnd
	case anthropic.StopReasonMaxTokens:
		return message.StopLength
	case anthropic.StopReasonToolUse:
		return message.StopToolInvoke
	}
	return ""
}
