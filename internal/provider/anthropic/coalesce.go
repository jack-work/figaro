package anthropic

import (
	"encoding/json"
)

// rowRole peeks a row's role without decoding its content.
func rowRole(raw json.RawMessage) string {
	var m struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Role
}

// mergeRows concatenates b's content onto a's. It reports false when either
// row cannot be read as a message, in which case the caller leaves both alone:
// a row we cannot parse is a row we must not rewrite.
func mergeRows(a, b json.RawMessage) (json.RawMessage, bool) {
	var ma, mb nativeMessage
	if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil {
		return nil, false
	}
	ma.Content = append(ma.Content, mb.Content...)
	merged, err := json.Marshal(ma)
	if err != nil {
		return nil, false
	}
	return merged, true
}
