package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// formRefs expands `@key!` from the aria's board. The sigil is configurable
// (cli.ref_sigil); the terminator is not. An unknown key refuses the message.
type formRefs struct{}

func (formRefs) Name() string { return "ref" }

func (formRefs) Rewrite(_ context.Context, view InputView, text string) (string, error) {
	sigil := refSigilOf(view.Settings)
	if !strings.ContainsRune(text, rune(sigil)) {
		return text, nil
	}
	var out strings.Builder
	out.Grow(len(text))
	i := 0
	for i < len(text) {
		c := text[i]
		if c != sigil {
			out.WriteByte(c)
			i++
			continue
		}
		key, n := readRefKey(text[i+1:])
		// Sigil, key, '!'. Anything else is text: an email address, a decorator.
		if key == "" || i+1+n >= len(text) || text[i+1+n] != '!' {
			out.WriteByte(c)
			i++
			continue
		}
		raw, ok := view.Form.Get(key)
		if !ok {
			return "", fmt.Errorf("%c%s! is not on the board", sigil, key)
		}
		out.WriteString(refValueString(raw))
		i += 1 + n + 1
	}
	return out.String(), nil
}

// A bad sigil is refused at CLI startup; the daemon must not refuse every message over it.
func refSigilOf(l interface{ RefSigil() (string, error) }) byte {
	if l == nil {
		return '@'
	}
	s, err := l.RefSigil()
	if err != nil || len(s) != 1 {
		return '@'
	}
	return s[0]
}

// readRefKey returns the key and bytes consumed, or "" when the head is not a key.
func readRefKey(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	if c := s[0]; !(refAlpha(c) || c == '_') {
		return "", 0
	}
	end := 1
	for end < len(s) {
		c := s[end]
		if refAlpha(c) || refDigit(c) || c == '_' || c == '.' {
			end++
			continue
		}
		break
	}
	for end > 1 && s[end-1] == '.' {
		end--
	}
	return s[:end], end
}

func refAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func refDigit(b byte) bool { return b >= '0' && b <= '9' }

// refValueString renders a string as itself, anything else as compact JSON.
func refValueString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
