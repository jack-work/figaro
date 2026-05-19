package anthropicsdk

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/message"
)

// decodeAssistantMessage projects an SDK Message (the final
// accumulated assistant turn) to the figaro IR.
func decodeAssistantMessage(m anthropic.Message) message.Message {
	out := message.Message{
		Role:     message.RoleAssistant,
		Provider: providerName,
		Model:    string(m.Model),
	}
	for _, b := range m.Content {
		switch v := b.AsAny().(type) {
		case anthropic.TextBlock:
			out.Content = append(out.Content, message.Content{Type: message.ContentText, Text: v.Text})
		case anthropic.ThinkingBlock:
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
