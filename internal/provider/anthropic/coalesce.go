package anthropic

import (
	"encoding/json"
)

func rowRole(raw json.RawMessage) string {
	var m struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Role
}

// resultsFirst stably hoists tool_result blocks to the head of a user turn:
// the API refuses a text block in front of them.
func resultsFirst(blocks []nativeBlock) []nativeBlock {
	first := -1
	for i, b := range blocks {
		if b.Type == "tool_result" {
			first = i
			break
		}
	}
	if first <= 0 {
		return blocks
	}
	out := make([]nativeBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "tool_result" {
			out = append(out, b)
		}
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			out = append(out, b)
		}
	}
	return out
}

// mergeRows reports false when either row does not parse; the caller then
// leaves both alone.
func mergeRows(a, b json.RawMessage) (json.RawMessage, bool) {
	var ma, mb nativeMessage
	if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil {
		return nil, false
	}
	ma.Content = append(ma.Content, mb.Content...)
	if ma.Role == "user" {
		ma.Content = resultsFirst(ma.Content)
	}
	merged, err := json.Marshal(ma)
	if err != nil {
		return nil, false
	}
	return merged, true
}
