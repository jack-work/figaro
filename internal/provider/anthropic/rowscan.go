package anthropic

import (
	"bytes"
	"encoding/json"
)

type blockRef struct {
	typ, id, toolUseID string
}

var roleHead = []byte(`{"role":"`)

func headRole(raw []byte) (string, bool) {
	if !bytes.HasPrefix(raw, roleHead) {
		return "", false
	}
	rest := raw[len(roleHead):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 || bytes.IndexByte(rest[:end], '\\') >= 0 {
		return "", false
	}
	return string(rest[:end]), true
}

func scanBlocks(row []byte) ([]blockRef, bool) {
	var blocks []blockRef
	found := false
	ok := eachMember(row, 0, func(key []byte, v int, end int) bool {
		if !bytes.EqualFold(key, []byte("content")) {
			return true
		}
		if !bytes.Equal(key, []byte("content")) || row[v] != '[' {
			return false
		}
		found = true
		blocks = blocks[:0]
		return eachElement(row, v, func(at, endAt int) bool {
			if row[at] != '{' {
				return false
			}
			var b blockRef
			if !eachMember(row, at, func(k []byte, bv int, bend int) bool {
				var dst *string
				switch {
				case bytes.Equal(k, []byte("type")):
					dst = &b.typ
				case bytes.Equal(k, []byte("id")):
					dst = &b.id
				case bytes.Equal(k, []byte("tool_use_id")):
					dst = &b.toolUseID
				case bytes.EqualFold(k, []byte("type")), bytes.EqualFold(k, []byte("id")), bytes.EqualFold(k, []byte("tool_use_id")):
					return false
				default:
					return true
				}
				s, ok := stringValue(row[bv:bend])
				if !ok {
					return false
				}
				if s != nil {
					*dst = *s
				}
				return true
			}) {
				return false
			}
			blocks = append(blocks, b)
			return true
		})
	})
	return blocks, ok && found
}

func stringValue(v []byte) (*string, bool) {
	if bytes.Equal(v, []byte("null")) {
		return nil, true
	}
	if len(v) < 2 || v[0] != '"' {
		return nil, false
	}
	if bytes.IndexByte(v, '\\') < 0 {
		s := string(v[1 : len(v)-1])
		return &s, true
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return nil, false
	}
	return &s, true
}

func eachMember(b []byte, at int, fn func(key []byte, v, end int) bool) bool {
	i := skipSpace(b, at)
	if i >= len(b) || b[i] != '{' {
		return false
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return true
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return false
		}
		kend, ok := valueEnd(b, i)
		if !ok {
			return false
		}
		key := b[i+1 : kend-1]
		if bytes.IndexByte(key, '\\') >= 0 {
			return false
		}
		i = skipSpace(b, kend)
		if i >= len(b) || b[i] != ':' {
			return false
		}
		v := skipSpace(b, i+1)
		end, ok := valueEnd(b, v)
		if !ok || !fn(key, v, end) {
			return false
		}
		i = skipSpace(b, end)
		if i >= len(b) {
			return false
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case '}':
			return true
		default:
			return false
		}
	}
}

func eachElement(b []byte, at int, fn func(at, end int) bool) bool {
	i := skipSpace(b, at+1)
	if i < len(b) && b[i] == ']' {
		return true
	}
	for {
		end, ok := valueEnd(b, i)
		if !ok || !fn(i, end) {
			return false
		}
		i = skipSpace(b, end)
		if i >= len(b) {
			return false
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case ']':
			return true
		default:
			return false
		}
	}
}

func valueEnd(b []byte, at int) (int, bool) {
	if at >= len(b) {
		return 0, false
	}
	switch b[at] {
	case '"':
		for i := at + 1; i < len(b); i++ {
			switch b[i] {
			case '\\':
				i++
			case '"':
				return i + 1, true
			}
		}
		return 0, false
	case '{', '[':
		depth := 0
		inString := false
		for i := at; i < len(b); i++ {
			if inString {
				switch b[i] {
				case '\\':
					i++
				case '"':
					inString = false
				}
				continue
			}
			switch b[i] {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	}
	for i := at; i < len(b); i++ {
		switch b[i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return i, i > at
		}
	}
	return 0, false
}

func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}
