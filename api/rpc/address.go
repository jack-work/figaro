package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const AriaKey = "figaro_id"

const MethodFollow = "figaro.follow"

type FollowResponse struct {
	OK       bool   `json:"ok"`
	FigaroID string `json:"figaro_id,omitempty"`
}

const MethodAriaClosed = "figaro.closed"

type AriaClosed struct {
	FigaroID string `json:"figaro_id"`
}

func MethodFollows(method string) bool {
	switch method {
	case MethodQua, MethodFollow:
		return true
	}
	return false
}

func WithAria(params json.RawMessage, id string) (json.RawMessage, error) {
	key, err := json.Marshal(AriaKey)
	if err != nil {
		return nil, err
	}
	val, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage(`{` + string(key) + `:` + string(val) + `}`), nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("params must be a JSON object to carry %s", AriaKey)
	}
	if bytes.Contains(trimmed, key) {
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, err
		}
		fields[AriaKey] = val
		return json.Marshal(fields)
	}
	out := make([]byte, 0, len(trimmed)+len(key)+len(val)+2)
	out = append(out, '{')
	out = append(out, key...)
	out = append(out, ':')
	out = append(out, val...)
	rest := bytes.TrimSpace(trimmed[1:])
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	out = append(out, rest...)
	return out, nil
}

func AriaOf(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	if id, ok := leadingAria(params); ok {
		return id
	}
	var a struct {
		ID string `json:"figaro_id"`
	}
	if err := json.Unmarshal(params, &a); err != nil {
		return ""
	}
	return a.ID
}

var ariaLead = []byte(`{"` + AriaKey + `":"`)

func leadingAria(params json.RawMessage) (string, bool) {
	if !bytes.HasPrefix(params, ariaLead) {
		return "", false
	}
	rest := params[len(ariaLead):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 || ValidateAriaID(string(rest[:end])) != nil {
		return "", false
	}
	after := bytes.TrimLeft(rest[end+1:], " \t\r\n")
	if len(after) == 0 || (after[0] != ',' && after[0] != '}') {
		return "", false
	}
	if mayNameAria(after) {
		return "", false
	}
	return string(rest[:end]), true
}

func mayNameAria(b []byte) bool {
	for {
		i := bytes.IndexByte(b, '_')
		if i < 0 {
			return false
		}
		b = b[i+1:]
		if len(b) >= 3 && bytes.EqualFold(b[:2], []byte("id")) && b[2] == '"' {
			return true
		}
	}
}
