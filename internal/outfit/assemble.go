package outfit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jack-work/figaro/api/form"
)

// NameDefault, as an outfit name, means whatever config calls the default outfit.
const NameDefault = "default"

// ParseNames reads the `-O` syntax: comma-separated outfit names, later winning.
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

// ParseSet reads the `-S` syntax: `k=v` pairs and JSON-object literals,
// comma-separated, later winning. A `layers` key here is ordinary data.
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
	return form.Build(form.Snapshot{}, set, nil), nil
}

// ParseDelete reads the `-D` syntax: comma-separated key paths, unvalidated.
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

// Dress folds names in order, then the caller's patch on top; removals ride through.
func (o *Outfitter) Dress(names []string, patch form.Patch, defaultName string) (form.Patch, error) {
	if len(names) == 0 {
		return patch, nil
	}
	// Compose as key sets and build once: merging per-layer patches would
	// clobber subtrees at the first shared segment.
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
		for _, e := range folded.Entries() {
			if !e.IsRemoval() {
				layered[e.Key] = e.New
			}
		}
	}
	var drops []string
	for _, e := range patch.Entries() {
		if e.IsRemoval() {
			drops = append(drops, e.Key)
			continue
		}
		layered[e.Key] = e.New
	}
	return form.Build(form.Snapshot{}, layered, drops), nil
}

func (o *Outfitter) defaults(defaultName string) (form.Patch, error) {
	if defaultName == "" {
		return form.Patch{}, nil
	}
	names, err := TermNames(defaultName)
	if err != nil {
		return form.Patch{}, err
	}
	out := map[string]json.RawMessage{}
	for _, n := range names {
		folded, ferr := o.LoadOptional(n)
		if ferr != nil {
			return form.Patch{}, ferr
		}
		for _, e := range folded.Entries() {
			if !e.IsRemoval() {
				out[e.Key] = e.New
			}
		}
	}
	return form.Build(form.Snapshot{}, out, nil), nil
}

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
	return form.Build(form.Snapshot{}, set, nil), nil
}

func (o *Outfitter) nameKeys(name string) (map[string]json.RawMessage, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	patch, err := o.Load(name)
	if err != nil {
		return nil, err
	}
	keys := map[string]json.RawMessage{}
	for _, e := range patch.Entries() {
		if !e.IsRemoval() {
			keys[e.Key] = e.New
		}
	}
	return keys, nil
}

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

// The value is JSON when it parses as JSON, a plain string otherwise.
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

func sugarHint(part string) string {
	body := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
	k, v, ok := strings.Cut(body, ":")
	if !ok || strings.ContainsAny(body, `"{}[],`) || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
		return ""
	}
	return fmt.Sprintf(" (for a one-key literal, write %s=%s)", strings.TrimSpace(k), strings.TrimSpace(v))
}

// ValidName: a name is a file basename, so path separators and the grammar's punctuation are refused.
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
		// Usually a shell-brace-expanded literal that lost its braces.
		return fmt.Errorf("outfit: %q: a name cannot contain `:`: for a literal, quote it ('{\"a\":1}') or use the sugar (a=1)", name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("outfit: %q: a name cannot contain whitespace", name)
		}
	}
	return nil
}

// splitTerms splits on commas outside quotes, braces and brackets.
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

// TermNames returns just the names in a spec string.
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

// ResolveAll builds a synthetic root whose layers are these names, in order.
func (o *Outfitter) ResolveAll(names []string) *Closure {
	root := &Closure{Found: true}
	memo := map[string]*Closure{}
	ep := o.current()
	for _, n := range names {
		root.Layers = append(root.Layers, o.resolveNode(ep, n, nil, memo))
	}
	return root
}
