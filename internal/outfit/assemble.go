package outfit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// KeyLayers is the patch key that names outfits to fold in. It is a DIRECTIVE,
// resolved by the server's Materialize and never left on a board: a client
// writes names, the server writes the keys they stand for. That is what lets
// layers work on a patch applied long after birth, not only at mint time.
const KeyLayers = "layers"

// NameDefault, as a layer, means whatever config calls the default outfit. It
// is how a client asks for "dressed as usual" without knowing the answer.
const NameDefault = "default"

// ParsePatch turns the `-O` syntax into ONE chalkboard patch. It touches no
// disk and reads no config: a name becomes an entry in `layers` for the server
// to resolve, a literal or `k=v` becomes keys.
//
//	sonn5,focus                 {"layers":["sonn5","focus"]}
//	ttl=1h,mantra="cool thing"  {"ttl":"1h","mantra":"cool thing"}
//	sonn5,{"ttl":"1h"}          {"layers":["sonn5"],"ttl":"1h"}
//
// Names keep their order. A literal does NOT interleave with the layers a later
// name pulls in — `a,{x:1},b` folds a and b first, then x — which is a
// documented gap, not a defended property.
func ParsePatch(text string) (chalkboard.Patch, error) {
	parts, err := splitTerms(text)
	if err != nil {
		return chalkboard.Patch{}, err
	}
	set := map[string]json.RawMessage{}
	var layers []string
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
			if err = ValidName(part); err != nil {
				return chalkboard.Patch{}, err
			}
			layers = append(layers, part)
		}
		if err != nil {
			return chalkboard.Patch{}, err
		}
		for k, v := range keys {
			set[k] = v
		}
	}
	// A literal may name layers of its own; they come before the ones typed
	// as bare names, since the literal was written first.
	if raw, ok := set[KeyLayers]; ok {
		var declared []string
		if err := json.Unmarshal(raw, &declared); err != nil {
			return chalkboard.Patch{}, fmt.Errorf("outfit: layers must be an array of names: %w", err)
		}
		layers = append(declared, layers...)
	}
	if len(layers) > 0 {
		b, err := json.Marshal(layers)
		if err != nil {
			return chalkboard.Patch{}, err
		}
		set[KeyLayers] = b
	}
	if len(set) == 0 {
		return chalkboard.Patch{}, nil
	}
	return chalkboard.Patch{Set: set}, nil
}

// Materialize expands a patch's `layers` directive into the keys those outfits
// set, and removes the directive. Layer keys go UNDER the patch's own: a client
// that wrote both meant its own value.
//
// This runs on the server for every patch — birth, set and fork dress alike —
// so `-O opus5` on a live aria means what it means at mint time.
//
// defaultName is what the reserved layer `default` stands for. An unset default
// folds nothing rather than failing: the first-run flow rides on that absence
// and notices the missing system.provider downstream. A default that names a
// file which does not exist yet is the same case. Every OTHER name is strict —
// a typo is a typo.
//
// TODO: realize only the DELTA of the closure. A patch that adds one layer to a
// board already wearing its siblings re-reads the whole graph; a DP over the
// layer graph keyed on what the board already holds would fold only what
// changed. Distant — outfits are small.
func (o *Outfitter) Materialize(patch chalkboard.Patch, defaultName string) (chalkboard.Patch, error) {
	names, ok, err := Layers(patch)
	if err != nil || !ok {
		return patch, err
	}
	layered := map[string]json.RawMessage{}
	for _, n := range names {
		var folded chalkboard.Patch
		var ferr error
		if n == NameDefault {
			folded, ferr = o.defaults(defaultName)
		} else {
			folded, ferr = o.Load(n)
		}
		if ferr != nil {
			return chalkboard.Patch{}, ferr
		}
		for k, v := range folded.Set {
			layered[k] = v
		}
	}
	out := chalkboard.Patch{Set: layered, Remove: patch.Remove}
	for k, v := range patch.Set {
		if k == KeyLayers {
			continue
		}
		out.Set[k] = v
	}
	return out, nil
}

// defaults folds what config calls the default outfit, leniently.
func (o *Outfitter) defaults(defaultName string) (chalkboard.Patch, error) {
	if defaultName == "" {
		return chalkboard.Patch{}, nil
	}
	names, err := TermNames(defaultName)
	if err != nil {
		return chalkboard.Patch{}, err
	}
	out := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	for _, n := range names {
		folded, ferr := o.LoadOptional(n)
		if ferr != nil {
			return chalkboard.Patch{}, ferr
		}
		for k, v := range folded.Set {
			out.Set[k] = v
		}
	}
	return out, nil
}

// Layers reads a patch's `layers` directive: the names, and whether it had one.
func Layers(patch chalkboard.Patch) ([]string, bool, error) {
	raw, ok := patch.Set[KeyLayers]
	if !ok {
		return nil, false, nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, true, fmt.Errorf("outfit: layers must be an array of names: %w", err)
	}
	for _, n := range names {
		if err := ValidName(n); err != nil {
			return nil, true, err
		}
	}
	return names, true, nil
}

// WithLayer prepends a layer to a patch's directive, so the caller's own layers
// keep precedence over it. Birth uses it to put `default` underneath.
func WithLayer(patch chalkboard.Patch, name string) (chalkboard.Patch, error) {
	names, _, err := Layers(patch)
	if err != nil {
		return chalkboard.Patch{}, err
	}
	b, err := json.Marshal(append([]string{name}, names...))
	if err != nil {
		return chalkboard.Patch{}, err
	}
	out := chalkboard.Patch{Set: map[string]json.RawMessage{}, Remove: patch.Remove}
	for k, v := range patch.Set {
		out.Set[k] = v
	}
	out.Set[KeyLayers] = b
	return out, nil
}

// Label names a patch for a listing: the layers it folded, in order, with
// `default` resolved to what it stood for. Empty when nothing was named — a
// patch of bare keys has no outfit to be called after.
func Label(patch chalkboard.Patch, defaultName string) string {
	names, ok, err := Layers(patch)
	if !ok || err != nil {
		return ""
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != NameDefault {
			out = append(out, n)
			continue
		}
		if more, err := TermNames(defaultName); err == nil {
			out = append(out, more...)
		}
	}
	return strings.Join(out, ",")
}

// Names folds a list of outfit names, in order.
func (o *Outfitter) Names(names ...string) (chalkboard.Patch, error) {
	set := map[string]json.RawMessage{}
	for _, name := range names {
		keys, err := o.nameKeys(name)
		if err != nil {
			return chalkboard.Patch{}, err
		}
		for k, v := range keys {
			set[k] = v
		}
	}
	if len(set) == 0 {
		return chalkboard.Patch{}, nil
	}
	return chalkboard.Patch{Set: set}, nil
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
	for _, n := range names {
		root.Layers = append(root.Layers, o.resolve(n, nil, memo))
	}
	return root
}
