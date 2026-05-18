// Package cli — @key chalkboard reference expansion.
package cli

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/transport"
)

// atExpandTimeout bounds the snapshot fetch used by @-expansion. The
// figaro daemon should answer Chalkboard() in microseconds for a
// resident aria; this is the budget for the entire round-trip
// including dial. On timeout / dial error / any failure, we fall
// back to leaving @-references literal (permissive mode).
const atExpandTimeout = 500 * time.Millisecond

// expandAtRefs substitutes @key references in prompt with their
// string values from snap. Permissive: unknown keys are left literal
// (an @ followed by a non-key word, e.g. "me@example.com", is also
// left untouched so common false-positives don't break prompts).
//
// Reference grammar:
//
//	@ <key>
//	key = [a-zA-Z_] [a-zA-Z0-9_.]*
//
// Brace form (@{...}) is NOT supported; only bare @key. A reference
// is recognized only at a word boundary: the character immediately
// before @ must be start-of-string or a non-word character (anything
// that is not a letter, digit, or underscore). This keeps email
// addresses literal (`me@example.com` — letter before @) while still
// permitting punctuation-bounded refs like `=@count` and `(@cwd)`.
//
// Non-string snapshot values are rendered via JSON unmarshal into
// any: strings unwrap to their text, everything else is rendered via
// fmt.Sprintf("%v"). Empty snapshot is a valid no-op.
func expandAtRefs(prompt string, snap map[string]json.RawMessage) string {
	if len(snap) == 0 || !strings.ContainsRune(prompt, '@') {
		return prompt
	}
	var out strings.Builder
	out.Grow(len(prompt))
	i := 0
	for i < len(prompt) {
		c := prompt[i]
		if c != '@' {
			out.WriteByte(c)
			i++
			continue
		}
		// Word-boundary check: the @ must be at start-of-string or
		// directly after whitespace. Anything else (letter, digit,
		// punctuation) means we're in the middle of a token like
		// "me@example.com" — leave the @ literal.
		if i > 0 && !isExpansionBoundary(prompt[i-1]) {
			out.WriteByte(c)
			i++
			continue
		}
		key, advance := readAtKey(prompt[i+1:])
		if key == "" {
			out.WriteByte(c)
			i++
			continue
		}
		raw, ok := snap[key]
		if !ok {
			// Permissive: unknown key left literal so typos and
			// non-references (e.g. email addresses we didn't catch
			// at the boundary check) pass through untouched.
			out.WriteByte(c)
			i++
			continue
		}
		out.WriteString(snapshotValueToString(raw))
		i += 1 + advance
	}
	return out.String()
}

// isExpansionBoundary reports whether b is a character that can
// precede a recognized @key reference. Anything that's NOT a letter,
// digit, or underscore qualifies. This keeps email addresses literal
// (`me@example.com` — letter before @) while still permitting
// punctuation-bounded refs like `=@count`, `(@cwd)`, `"@model"`.
func isExpansionBoundary(b byte) bool {
	return !(isAlpha(b) || isDigit(b) || b == '_')
}

// readAtKey reads a chalkboard key off the head of s and returns the
// key and the number of bytes consumed. Returns "" if the head does
// not look like a key.
func readAtKey(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	// First byte must be a letter or underscore.
	c := s[0]
	if !(isAlpha(c) || c == '_') {
		return "", 0
	}
	end := 1
	for end < len(s) {
		c := s[end]
		if isAlpha(c) || isDigit(c) || c == '_' || c == '.' {
			end++
			continue
		}
		break
	}
	// A key cannot end with a "." (that's a dotted-path separator,
	// not a terminator). Walk back if needed.
	for end > 1 && s[end-1] == '.' {
		end--
	}
	return s[:end], end
}

func isAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// snapshotValueToString renders a snapshot value as a single string
// suitable for inline substitution. Strings unwrap to their text;
// everything else is JSON-decoded and rendered with the default
// fmt verb. On unmarshal failure, the raw JSON bytes are used.
func snapshotValueToString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return formatAny(v)
	}
	return string(raw)
}

func formatAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		// Reasonable default for numbers, bools, arrays, maps.
		// Marshal back to JSON for a compact, deterministic form.
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// fetchSnapshotForEndpoint best-effort dials a figaro at ep and
// fetches its chalkboard snapshot. Returns nil on any failure; the
// caller treats nil as "no expansion possible" (permissive mode).
func fetchSnapshotForEndpoint(parent context.Context, ep transport.Endpoint) map[string]json.RawMessage {
	ctx, cancel := context.WithTimeout(parent, atExpandTimeout)
	defer cancel()
	fcli, err := figaro.DialClient(ep, nil)
	if err != nil {
		return nil
	}
	defer fcli.Close()
	resp, err := fcli.Chalkboard(ctx)
	if err != nil {
		return nil
	}
	return resp.Snapshot
}

// expandAtRefsForEndpoint is the convenience wrapper used by the
// prompt entry points: fetch the snapshot for ep and substitute @key
// references in prompt. Safe to call with a nil-ish endpoint; falls
// through to the unexpanded prompt on any failure.
func expandAtRefsForEndpoint(ctx context.Context, ep transport.Endpoint, prompt string) string {
	if !strings.ContainsRune(prompt, '@') {
		return prompt
	}
	snap := fetchSnapshotForEndpoint(ctx, ep)
	if snap == nil {
		return prompt
	}
	return expandAtRefs(prompt, snap)
}
