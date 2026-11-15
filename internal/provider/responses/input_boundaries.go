package responses

import (
	"encoding/json"
	"strings"
)

func separateInputTextParts(input []json.RawMessage) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		if kind != "message" || role != "user" {
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &parts) != nil {
			continue
		}
		changed := false
		for j := 1; j < len(parts); j++ {
			var previous, current string
			_ = json.Unmarshal(parts[j-1]["type"], &previous)
			_ = json.Unmarshal(parts[j]["type"], &current)
			if previous != "input_text" || current != "input_text" {
				continue
			}
			var text string
			if json.Unmarshal(parts[j]["text"], &text) != nil || text == "" {
				continue
			}
			if strings.HasPrefix(text, "\n\n") {
				continue
			}
			parts[j]["text"], _ = json.Marshal("\n\n" + text)
			changed = true
		}
		if !changed {
			continue
		}
		item["content"], _ = json.Marshal(parts)
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = append([]json.RawMessage(nil), input...)
		}
		out[i] = encoded
	}
	if out == nil {
		return input, nil
	}
	return out, nil
}
