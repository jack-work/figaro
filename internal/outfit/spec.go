package outfit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
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

func (s Spec) IsEmpty() bool { return len(s) == 0 }

// MaxInlineBytes caps the literals in one spec. An outfit file may be as large
// as its skills need, but an inline term is argv: one fold is ONE chalkboard
// record, and a record larger than a WAL segment (1 MiB floor) cannot be
// appended at all. A big literal wants a file with a name.
const MaxInlineBytes = 64 << 10

// Validate rejects a spec that cannot reasonably be applied. It runs on the
// server too, because a Spec can arrive as JSON without passing ParseSpec.
func (s Spec) Validate() error {
	total := 0
	for _, t := range s {
		if t.Name != "" {
			if err := ValidName(t.Name); err != nil {
				return err
			}
			continue
		}
		if len(t.Inline) == 0 {
			return fmt.Errorf("outfit: an inline term sets nothing (%s)", t)
		}
		b, err := json.Marshal(t.Inline)
		if err != nil {
			return fmt.Errorf("outfit: inline term: %w", err)
		}
		total += len(b)
	}
	if total > MaxInlineBytes {
		return fmt.Errorf("outfit: inline terms are %d bytes, over the %d-byte limit — put them in an outfit file",
			total, MaxInlineBytes)
	}
	return nil
}

// Label is what an aria born on this spec is stamped with, and the stump
// directory it lives under. It is the NAMES in order, plus a single `{}` when
// any term was a literal.
//
// The stump id is `<label>@<hash of the folded patch>`, so the label must not
// separate what the content unites. Literals are anonymous — the hash already
// says everything about them — and where they sit changes the FOLD, not the
// identity: `base,x=1` and `x=1,base` fold to one patch when they do not
// collide, and must then be one stump. Rendering their positions minted a
// second stump with a byte-identical birth record: another gc entry, and a
// split provider cache prefix for no reason.
//
// `{}` is deliberately something ValidName refuses, so a stamp carrying a
// literal cannot be re-parsed as a spec — which is what makes "this version is
// not re-resolvable" enforced rather than hoped for.
func (s Spec) Label() string {
	parts := make([]string, 0, len(s)+1)
	inline := false
	for _, t := range s {
		if t.Name == "" {
			inline = true
			continue
		}
		parts = append(parts, t.Name)
	}
	if inline {
		parts = append(parts, "{}")
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

// ParseSpec reads the CLI syntax. Terms split on top-level commas; commas
// inside quotes, braces or brackets are data.
//
//	sonn5,focus                 names, folded left to right
//	ttl=1h,mantra="cool thing"  sugar: one inline term each
//	{"ttl":"1h"}                the literal it stands for; layers allowed
//
// A k=v value that parses as JSON keeps its type (3, true, [1,2]); anything
// else is a string.
func ParseSpec(text string) (Spec, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return nil, err
	}
	var out Spec
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case strings.HasPrefix(part, "{"):
			var inline map[string]any
			if err := json.Unmarshal([]byte(part), &inline); err != nil {
				return nil, fmt.Errorf("outfit: %s: not a JSON object%s: %w", part, sugarHint(part), err)
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
	return out, out.Validate()
}

// sugarHint answers the commonest way a literal is mistyped: unquoted keys,
// `{mantra:test}`, which is not JSON and is spelled `mantra=test` here. The
// braces are the worse half of that mistake — see ValidName on `:`.
func sugarHint(part string) string {
	body := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
	k, v, ok := strings.Cut(body, ":")
	if !ok || strings.ContainsAny(body, `"{}[],`) || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
		return ""
	}
	return fmt.Sprintf(" (for a one-key literal, write %s=%s)", strings.TrimSpace(k), strings.TrimSpace(v))
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
		// A value that OPENS a quote and does not parse is an unterminated
		// string, not a string that happens to start with a quote: taking it
		// literally would silently store the quote.
		if strings.HasPrefix(raw, `"`) {
			return "", nil, fmt.Errorf("outfit: %s: unterminated quoted value", part)
		}
		return key, raw, nil
	}
	return key, value, nil
}

// ValidName is the gate every name passes, in a spec or in a layers list. A
// name is a file basename, so it is narrow: `=` is the sugar's separator, a
// path separator would climb out of the outfits directory, and the grammar's
// own punctuation would otherwise arrive as a name that can only be missing.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("outfit: empty name")
	case name == "." || name == "..":
		return fmt.Errorf("outfit: %q is not a name", name)
	case strings.HasPrefix(name, "-"):
		// `-O -j` used to make an outfit called "-j" and fail one round trip
		// later, on the server, in the wrong vocabulary.
		return fmt.Errorf("outfit: %q looks like a flag, not an outfit (--outfit takes a value)", name)
	case strings.ContainsAny(name, "=/\\"):
		return fmt.Errorf("outfit: %q: a name cannot contain = / or \\", name)
	case strings.ContainsAny(name, `{}[]"`):
		return fmt.Errorf("outfit: %q: a name cannot contain {} [] or a quote (an inline term must be a whole JSON object)", name)
	case strings.Contains(name, ":"):
		// `-O {a:1,b:2}` unquoted is brace-EXPANDED by the shell into two
		// words with the braces gone, so this is usually a literal that never
		// reached us. `:` is also the turn coordinate, so it can never be part
		// of a name anyway.
		return fmt.Errorf("outfit: %q: a name cannot contain `:` — for a literal, quote it ('{\"a\":1}') or use the sugar (a=1)", name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("outfit: %q: a name cannot contain whitespace", name)
		}
	}
	return nil
}

// splitTerms splits on commas that are not inside quotes, braces or brackets.
// Unbalanced structure is an error rather than a mode: a stray `}` used to
// drive depth negative and a stray `"` used to flip quoting on, and from there
// the commas stopped separating and the whole tail arrived as one "name".
func splitTerms(text string) ([]string, error) {
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
			if depth < 0 {
				return nil, fmt.Errorf("outfit: %s: unbalanced %q", text, r)
			}
		case r == ',' && depth == 0:
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if quote {
		return nil, fmt.Errorf("outfit: %s: unterminated quote", text)
	}
	if depth != 0 {
		return nil, fmt.Errorf("outfit: %s: unbalanced { or [", text)
	}
	out = append(out, cur.String())
	return out, nil
}
