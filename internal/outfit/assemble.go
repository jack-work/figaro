package outfit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jack-work/figaro/internal/form"
)

// NameDefault, as an outfit name, means whatever config calls the default
// outfit. It is how a client asks for "dressed as usual" without knowing the
// answer.
const NameDefault = "default"

// The grammar, since 2026-08-11 (Gluck's ruling): outfits and patches are
// SEPARATE AXES, and neither is smuggled inside the other.
//
//	-O sonn5,focus            names only        → ParseNames
//	-S ttl=1h,{"n":3}         keys only         → ParseSet
//	-D system.tags,mantra     key paths only    → ParseDelete
//
// They compose in one call, and the order is fixed: outfits fold first, then
// --set, then --delete. `layers` survives in exactly one place — the unmarshal
// that builds a patch from an outfit FILE — so a patch is data all the way
// down and no writer below the API boundary ever touches a disk.

// ParseNames reads the `-O` syntax: a comma-separated list of outfit names,
// in order, later names winning. Nothing else is admitted — a `k=v` or a JSON
// literal here is a grammar error naming the flag that takes it.
func ParseNames(text string) ([]string, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case strings.HasPrefix(part, "{"):
			return nil, fmt.Errorf("outfit: %s: --outfit takes outfit NAMES; a JSON literal goes in --set", part)
		case strings.ContainsRune(part, '='):
			return nil, fmt.Errorf("outfit: %s: --outfit takes outfit NAMES; `k=v` goes in --set (-S %s)", part, part)
		}
		if err := ValidName(part); err != nil {
			return nil, err
		}
		names = append(names, part)
	}
	return names, nil
}

// ParseSet reads the `-S` syntax into ONE form patch: `k=v` pairs and whole
// JSON-object literals, comma-separated, later terms winning. It touches no
// disk and reads no config, and it resolves nothing — a `layers` key written
// here is ordinary data, stored as typed.
//
//	ttl=1h,mantra="cool thing"  {"ttl":"1h","mantra":"cool thing"}
//	{"ttl":"1h"},n=3            {"ttl":"1h","n":3}
//
// A bare name is refused: that is an outfit, and outfits arrive through -O.
func ParseSet(text string) (form.Patch, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return form.Patch{}, err
	}
	set := map[string]json.RawMessage{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var keys map[string]json.RawMessage
		switch {
		case strings.HasPrefix(part, "{"):
			keys, err = literalKeys(part)
		case strings.ContainsRune(part, '='):
			keys, err = pairKeys(part)
		default:
			return form.Patch{}, fmt.Errorf("outfit: %s: --set takes `k=v` or a JSON literal; a bare name is an outfit (-O %s)", part, part)
		}
		if err != nil {
			return form.Patch{}, err
		}
		for k, v := range keys {
			set[k] = v
		}
	}
	if len(set) == 0 {
		return form.Patch{}, nil
	}
	return form.Patch{Set: set}, nil
}

// ParseDelete reads the `-D` syntax: comma-separated key paths to remove.
// Paths are not validated here beyond emptiness — the dotted/bracketed path
// grammar belongs to the form, and a key that does not exist is a no-op.
func ParseDelete(text string) ([]string, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "{") || strings.ContainsRune(part, '=') {
			return nil, fmt.Errorf("outfit: %s: --delete takes key paths, one per comma", part)
		}
		out = append(out, part)
	}
	return out, nil
}

// Dress is THE materialization call: the daemon's single point where outfit
// names become keys. Names fold in order, then the caller's patch lands on
// top — a client that wrote a key meant its own value — and the removals ride
// through untouched.
//
// It runs at the API boundary, ABOVE the store's single writer and above the
// agent's inbox, so everything below holds pure data and needs nothing from
// the filesystem. defaultName is what the reserved name `default` stands for;
// it alone is lenient (an unset or not-yet-written default folds nothing,
// which is what the first-run flow rides on, surfacing downstream as the
// missing provider it is). Every other name is strict — a typo is a typo.
func (o *Outfitter) Dress(names []string, patch form.Patch, defaultName string) (form.Patch, error) {
	if len(names) == 0 {
		return patch, nil
	}
	layered := map[string]json.RawMessage{}
	for _, n := range names {
		var folded form.Patch
		var err error
		if n == NameDefault {
			folded, err = o.defaults(defaultName)
		} else {
			folded, err = o.Load(n)
		}
		if err != nil {
			return form.Patch{}, err
		}
		for k, v := range folded.Set {
			layered[k] = v
		}
	}
	out := form.Patch{Set: layered, Remove: patch.Remove}
	for k, v := range patch.Set {
		out.Set[k] = v
	}
	return out, nil
}

// defaults folds what config calls the default outfit, leniently.
func (o *Outfitter) defaults(defaultName string) (form.Patch, error) {
	if defaultName == "" {
		return form.Patch{}, nil
	}
	names, err := TermNames(defaultName)
	if err != nil {
		return form.Patch{}, err
	}
	out := form.Patch{Set: map[string]json.RawMessage{}}
	for _, n := range names {
		folded, ferr := o.LoadOptional(n)
		if ferr != nil {
			return form.Patch{}, ferr
		}
		for k, v := range folded.Set {
			out.Set[k] = v
		}
	}
	return out, nil
}

// Names folds a list of outfit names, in order.
func (o *Outfitter) Names(names ...string) (form.Patch, error) {
	set := map[string]json.RawMessage{}
	for _, name := range names {
		keys, err := o.nameKeys(name)
		if err != nil {
			return form.Patch{}, err
		}
		for k, v := range keys {
			set[k] = v
		}
	}
	if len(set) == 0 {
		return form.Patch{}, nil
	}
	return form.Patch{Set: set}, nil
}

func (o *Outfitter) nameKeys(name string) (map[string]json.RawMessage, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	patch, err := o.Load(name)
	if err != nil {
		return nil, err
	}
	return patch.Set, nil
}

// literalKeys reads a JSON object term. `layers` inside a literal pulls in
// named outfits the same way a file's does — but resolving them is the
// assembler's job, and a literal that only declares layers sets nothing else.
func literalKeys(part string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(part), &raw); err != nil {
		return nil, fmt.Errorf("outfit: %s: not a JSON object%s: %w", part, sugarHint(part), err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("outfit: %s: sets nothing", part)
	}
	return raw, nil
}

// pairKeys reads `key=value`. The value is JSON when it parses as JSON and a
// plain string otherwise, so `ttl=1h` and `ttl="1h"` agree and `n=3` is a
// number.
func pairKeys(part string) (map[string]json.RawMessage, error) {
	i := strings.IndexRune(part, '=')
	key := strings.TrimSpace(part[:i])
	raw := strings.TrimSpace(part[i+1:])
	if key == "" {
		return nil, fmt.Errorf("outfit: %s: empty key", part)
	}
	if raw == "" {
		return map[string]json.RawMessage{key: json.RawMessage(`""`)}, nil
	}
	if !json.Valid([]byte(raw)) {
		// A value that OPENS a quote and does not parse is an unterminated
		// string, not a string that happens to start with a quote.
		if strings.HasPrefix(raw, `"`) {
			return nil, fmt.Errorf("outfit: %s: unterminated quoted value", part)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{key: b}, nil
	}
	return map[string]json.RawMessage{key: json.RawMessage(raw)}, nil
}

// sugarHint answers the commonest way a literal is mistyped: unquoted keys,
// `{mantra:test}`, which is not JSON and is spelled `mantra=test` here.
func sugarHint(part string) string {
	body := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
	k, v, ok := strings.Cut(body, ":")
	if !ok || strings.ContainsAny(body, `"{}[],`) || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
		return ""
	}
	return fmt.Sprintf(" (for a one-key literal, write %s=%s)", strings.TrimSpace(k), strings.TrimSpace(v))
}

// ValidName is the gate every name passes, in the syntax or in a layers list.
// A name is a file basename, so it is narrow: `=` is the sugar's separator, a
// path separator would climb out of the outfits directory, and the grammar's
// own punctuation would otherwise arrive as a name that can only be missing.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("outfit: empty name")
	case name == "." || name == "..":
		return fmt.Errorf("outfit: %q is not a name", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("outfit: %q looks like a flag, not an outfit (--outfit takes a value)", name)
	case strings.ContainsAny(name, "=/\\"):
		return fmt.Errorf("outfit: %q: a name cannot contain = / or \\", name)
	case strings.ContainsAny(name, `{}[]"`):
		return fmt.Errorf("outfit: %q: a name cannot contain {} [] or a quote (an inline term must be a whole JSON object)", name)
	case strings.Contains(name, ":"):
		// `-O {a:1,b:2}` unquoted is brace-EXPANDED by the shell into two
		// words with the braces gone, so this is usually a literal that never
		// reached us. `:` is also the turn coordinate.
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

// TermNames returns just the names in a spec string, for a caller that wants
// the layer graph (`state outfit --tree`) rather than the patch.
func TermNames(text string) ([]string, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "{") || strings.ContainsRune(part, '=') {
			continue
		}
		if err := ValidName(part); err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	return out, nil
}

// ResolveAll builds a synthetic root whose layers are these names, in order,
// for a caller that wants the layer graph rather than the patch.
func (o *Outfitter) ResolveAll(names []string) *Closure {
	root := &Closure{Found: true}
	memo := map[string]*Closure{}
	ep := o.current()
	for _, n := range names {
		root.Layers = append(root.Layers, o.resolveNode(ep, n, nil, memo))
	}
	return root
}
