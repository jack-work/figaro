package outfit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Spec is an ordered list of outfit terms, folded left to right: each term
// takes precedence over the ones before it, exactly as an outfit's own
// `layers = [...]` does.
//
// On the wire a Spec is a JSON array whose elements are either a name
// ("sonn5") or an inline literal ({"ttl":"1h"}). A bare JSON string is
// accepted too and parsed with ParseSpec, so the pre-spec scalar form
// ("outfit":"sonn5") still means what it always did.
type Spec []Term

// Term is one element of a Spec. Exactly one of Name or Inline is set.
type Term struct {
	Name   string
	Inline map[string]any
}

// Names builds a Spec from bare outfit names.
func Names(names ...string) Spec {
	out := make(Spec, 0, len(names))
	for _, n := range names {
		if n != "" {
			out = append(out, Term{Name: n})
		}
	}
	return out
}

// Inline builds a single-term Spec from a literal.
func InlineSpec(m map[string]any) Spec { return Spec{{Inline: m}} }

func (s Spec) IsEmpty() bool { return len(s) == 0 }

// Label is the name an aria born on this spec is stamped with. Inline terms
// have no name of their own, so they render as "inline"; the stump is
// content-addressed either way.
func (s Spec) Label() string {
	parts := make([]string, 0, len(s))
	for _, t := range s {
		if t.Name != "" {
			parts = append(parts, t.Name)
			continue
		}
		parts = append(parts, "inline")
	}
	return strings.Join(parts, ",")
}

// String renders the spec back into the CLI syntax that produced it.
func (s Spec) String() string {
	parts := make([]string, 0, len(s))
	for _, t := range s {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, ",")
}

func (t Term) String() string {
	if t.Name != "" {
		return t.Name
	}
	b, err := json.Marshal(t.Inline)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (t Term) MarshalJSON() ([]byte, error) {
	if t.Name != "" {
		return json.Marshal(t.Name)
	}
	if t.Inline == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(t.Inline)
}

func (t *Term) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, `"`) {
		var name string
		if err := json.Unmarshal(b, &name); err != nil {
			return err
		}
		if err := ValidName(name); err != nil {
			return err
		}
		*t = Term{Name: name}
		return nil
	}
	var inline map[string]any
	if err := json.Unmarshal(b, &inline); err != nil {
		return fmt.Errorf("outfit: term must be a name or an object: %w", err)
	}
	*t = Term{Inline: inline}
	return nil
}

func (s *Spec) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	switch {
	case trimmed == "" || trimmed == "null":
		*s = nil
		return nil
	case strings.HasPrefix(trimmed, `"`):
		var text string
		if err := json.Unmarshal(b, &text); err != nil {
			return err
		}
		parsed, err := ParseSpec(text)
		if err != nil {
			return err
		}
		*s = parsed
		return nil
	}
	var terms []Term
	if err := json.Unmarshal(b, &terms); err != nil {
		return err
	}
	*s = terms
	return nil
}

// ParseSpec reads the CLI syntax. Terms are separated by top-level commas
// (commas inside quotes, braces or brackets are data):
//
//	sonn5                       a named outfit on disk
//	sonn5,focus                 two, folded left to right
//	ttl=1h,mantra="cool thing"  key=value sugar, one inline term each
//	{"ttl":"1h"}                an inline literal, layers = [...] allowed
//
// The sugar is only sugar: k=v becomes the inline object {"k":v}, and a value
// that parses as JSON keeps its type (3, true, "quoted", [1,2]) while anything
// else is taken as a string.
func ParseSpec(text string) (Spec, error) {
	var out Spec
	for _, part := range splitTerms(text) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case strings.HasPrefix(part, "{"):
			var inline map[string]any
			if err := json.Unmarshal([]byte(part), &inline); err != nil {
				return nil, fmt.Errorf("outfit: %s: not a JSON object: %w", part, err)
			}
			out = append(out, Term{Inline: inline})
		case strings.ContainsRune(part, '='):
			k, v, err := parsePair(part)
			if err != nil {
				return nil, err
			}
			out = append(out, Term{Inline: map[string]any{k: v}})
		default:
			if err := ValidName(part); err != nil {
				return nil, err
			}
			out = append(out, Term{Name: part})
		}
	}
	return out, nil
}

// parsePair reads `key=value`. The value is JSON when it parses as JSON and a
// plain string otherwise, so `ttl=1h` and `ttl="1h"` agree and `n=3` is a
// number.
func parsePair(part string) (string, any, error) {
	i := strings.IndexRune(part, '=')
	key := strings.TrimSpace(part[:i])
	raw := strings.TrimSpace(part[i+1:])
	if key == "" {
		return "", nil, fmt.Errorf("outfit: %s: empty key", part)
	}
	var value any
	if raw == "" {
		return key, "", nil
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return key, raw, nil
	}
	return key, value, nil
}

// ValidName rejects the strings that cannot name an outfit file. `=` is
// reserved for the inline sugar and a separator would let a name climb out of
// the outfits directory.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("outfit: empty name")
	case strings.ContainsAny(name, "=/\\"):
		return fmt.Errorf("outfit: %q: a name cannot contain = / or \\", name)
	case strings.ContainsAny(name, "\n\t"):
		return fmt.Errorf("outfit: %q: a name cannot contain whitespace", name)
	case name == "." || name == "..":
		return fmt.Errorf("outfit: %q is not a name", name)
	}
	return nil
}

// splitTerms splits on commas that are not inside quotes, braces or brackets.
func splitTerms(text string) []string {
	var (
		out   []string
		depth int
		quote bool
		esc   bool
		cur   strings.Builder
	)
	for _, r := range text {
		switch {
		case esc:
			esc = false
		case quote && r == '\\':
			esc = true
		case r == '"':
			quote = !quote
		case quote:
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			depth--
		case r == ',' && depth == 0:
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	out = append(out, cur.String())
	return out
}
