// Package outfit assembles an aria's chalkboard from on-disk config.
//
// Load reads a named outfit TOML chain and returns a chalkboard patch.
// Providers read `system.credo` (and other system keys) straight off
// the chalkboard — no derivation step.
package outfit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Outfitter assembles chalkboards from on-disk outfits. Safe for concurrent
// use, and worth reusing: it caches what it reads against the files it read
// (see cache.go), so a repeat fold costs a stat per dependency.
type Outfitter struct {
	configDir string

	// layersOf caches an outfit's declared layers, keyed by file path.
	layersOf cache[[]string]
	// folded caches a fully composed patch, keyed by outfit name, against the
	// dependencies of its whole closure.
	folded cache[map[string]json.RawMessage]
}

// New returns an Outfitter rooted at configDir.
func New(configDir string) *Outfitter {
	return &Outfitter{configDir: configDir}
}

// Closure is one node of an outfit's layer graph: the outfit itself, the
// layers it declares, and whether each was actually found on disk. It is built
// even when layers are missing, because a broken reference is best explained by
// showing the shape it was found in.
type Closure struct {
	Name   string
	Path   string // "" when the outfit was not found
	Found  bool
	Cycle  bool // this name is already being resolved further up the chain
	Layers []*Closure
}

// Walk visits the closure depth-first, parents before layers.
func (c *Closure) Walk(fn func(*Closure)) {
	if c == nil {
		return
	}
	fn(c)
	for _, l := range c.Layers {
		l.Walk(fn)
	}
}

// MissingError reports that something in the closure is not on disk. It
// carries the whole closure so a caller can show where the gap is rather than
// only name it.
type MissingError struct {
	Closure *Closure
	Missing []string
	// RootOnly is true when the only thing missing is the outfit that was
	// asked for. That is the ordinary "no such outfit" and callers may treat
	// it as an absence rather than a fault — the first-run flow does.
	RootOnly bool
}

func (e *MissingError) Error() string {
	if len(e.Missing) == 1 {
		return fmt.Sprintf("outfit: %q does not exist", e.Missing[0])
	}
	return fmt.Sprintf("outfit: missing layers: %s", strings.Join(e.Missing, ", "))
}

// CycleError reports a layer that lists one of its own ancestors.
type CycleError struct {
	Closure *Closure
	At      string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("outfit: cycle in layers at %q", e.At)
}

// Load resolves one outfit and returns the chalkboard patch.
func (o *Outfitter) Load(name string) (chalkboard.Patch, error) {
	if name == "" {
		return chalkboard.Patch{}, nil
	}
	return o.LoadAll([]string{name})
}

// LoadAll folds several outfits into one patch, each taking precedence over
// the ones before it — the same rule that orders an outfit's own layers, so
// `figaro outfit a,b` and an outfit declaring `layers = ["a", "b"]` compose
// identically.
//
// An outfit that was ASKED for but does not exist yields an empty patch and no
// error: the first-run flow scaffolds config by letting that absence through
// and noticing the missing system.provider downstream. A layer referenced by an
// outfit that DOES exist is the opposite case — a broken reference, always an
// error, because the alternative is what this used to do: discard the whole
// patch and let a typo look like an empty outfit.
//
// Use LoadStrict where absence is a fault in itself.
func (o *Outfitter) LoadAll(names []string) (chalkboard.Patch, error) {
	return o.loadAll(names, false)
}

// LoadStrict is LoadAll with a missing outfit treated as a fault even when it
// is the one that was asked for. This is what applying an outfit by name wants:
// `figaro outfit nope` should say so, not quietly change nothing.
func (o *Outfitter) LoadStrict(names []string) (chalkboard.Patch, error) {
	return o.loadAll(names, true)
}

func (o *Outfitter) loadAll(names []string, strict bool) (chalkboard.Patch, error) {
	if len(names) == 0 {
		return chalkboard.Patch{}, nil
	}
	root := o.ResolveAll(names)
	if err := closureError(root, names); err != nil {
		var missing *MissingError
		if !strict && errors.As(err, &missing) && missing.RootOnly {
			return chalkboard.Patch{}, nil
		}
		return chalkboard.Patch{}, err
	}
	flat := map[string]json.RawMessage{}
	memo := map[string]foldResult{}
	for _, layer := range root.Layers {
		sub, err := o.fold(layer, memo)
		if err != nil {
			return chalkboard.Patch{}, err
		}
		for k, v := range sub.keys {
			flat[k] = v
		}
	}
	return chalkboard.Patch{Set: flat}, nil
}

// Resolve builds the closure for one outfit without reading any of it.
func (o *Outfitter) Resolve(name string) *Closure {
	return o.resolve(name, nil, map[string]*Closure{})
}

// ResolveAll builds a synthetic root whose layers are names, in order. The
// root is not an outfit and is never rendered as one.
func (o *Outfitter) ResolveAll(names []string) *Closure {
	root := &Closure{Found: true}
	memo := map[string]*Closure{}
	for _, n := range names {
		root.Layers = append(root.Layers, o.resolve(n, nil, memo))
	}
	return root
}

// resolve reads one outfit's layer list and recurses. stack is the chain
// currently being resolved, so a name that reappears on it is a cycle rather
// than a second visit; memo holds finished nodes, which by definition are not
// on the stack.
func (o *Outfitter) resolve(name string, stack []string, memo map[string]*Closure) *Closure {
	for _, s := range stack {
		if s == name {
			return &Closure{Name: name, Found: true, Cycle: true}
		}
	}
	if c, ok := memo[name]; ok {
		return c
	}
	c := &Closure{Name: name}
	path, err := o.resolvePath(name)
	if err != nil {
		memo[name] = c
		return c
	}
	c.Path, c.Found = path, true

	names, lErr := o.declaredLayers(path)
	if lErr != nil {
		// A malformed layers list is reported by fold, which parses the file
		// for real. The closure just stops descending.
		memo[name] = c
		return c
	}
	for _, l := range names {
		c.Layers = append(c.Layers, o.resolve(l, append(stack, name), memo))
	}
	memo[name] = c
	return c
}

// declaredLayers reads just the layers key from an outfit file. Cached against
// the file, so resolving a closure re-parses nothing that has not changed.
func (o *Outfitter) declaredLayers(path string) ([]string, error) {
	if names, ok := o.layersOf.get(path); ok {
		return names, nil
	}
	raw := map[string]any{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("outfit: parse %s: %w", path, err)
	}
	names, err := layerNames(path, raw)
	if err != nil {
		return nil, err
	}
	o.layersOf.put(path, names, []dep{statDep(path)})
	return names, nil
}

// layerNames extracts and validates the layers key.
//
// `source` was the single-parent spelling this replaced. It is rejected rather
// than ignored: left alone it would flatten into a chalkboard key named
// "source", which is the silent kind of wrong.
func layerNames(path string, raw map[string]any) ([]string, error) {
	if _, ok := raw["source"]; ok {
		return nil, fmt.Errorf("outfit: %s: `source` is no longer read; use `layers = [\"name\"]`", path)
	}
	value, ok := raw["layers"]
	if !ok {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("outfit: %s: layers must be an array of outfit names, got %T", path, value)
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("outfit: %s: layers[%d] must be a string, got %T", path, i, item)
		}
		if name == "" {
			return nil, fmt.Errorf("outfit: %s: layers[%d] is empty", path, i)
		}
		out = append(out, name)
	}
	return out, nil
}

// closureError reports the first structural fault in a closure: a cycle, or
// anything not on disk.
func closureError(root *Closure, requested []string) error {
	var cycle string
	var missing []string
	root.Walk(func(c *Closure) {
		if c.Cycle && cycle == "" {
			cycle = c.Name
		}
		if !c.Found {
			missing = append(missing, c.Name)
		}
	})
	if cycle != "" {
		return &CycleError{Closure: root, At: cycle}
	}
	if len(missing) == 0 {
		return nil
	}
	rootOnly := true
	for _, layer := range root.Layers {
		if layer.Found {
			rootOnly = false
		}
	}
	if len(requested) != len(missing) {
		rootOnly = false
	}
	return &MissingError{Closure: root, Missing: missing, RootOnly: rootOnly}
}

// fold returns one outfit's flattened keys: its layers in order, then its own,
// so the nearest declaration wins. Memoised per name, which makes a layer
// shared by several others cheap to apply at each of its positions.
func (o *Outfitter) fold(c *Closure, memo map[string]foldResult) (foldResult, error) {
	if done, ok := memo[c.Name]; ok {
		return done, nil
	}
	if keys, ok := o.folded.get(c.Name); ok {
		done := foldResult{keys: keys, deps: o.foldedDeps(c.Name)}
		memo[c.Name] = done
		return done, nil
	}
	flat := map[string]json.RawMessage{}
	d := &deps{}
	for _, layer := range c.Layers {
		sub, err := o.fold(layer, memo)
		if err != nil {
			return foldResult{}, err
		}
		for k, v := range sub.keys {
			flat[k] = v
		}
		d.merge(sub.deps)
	}
	raw := map[string]any{}
	if _, err := toml.DecodeFile(c.Path, &raw); err != nil {
		return foldResult{}, fmt.Errorf("outfit: parse %s: %w", c.Path, err)
	}
	if _, err := layerNames(c.Path, raw); err != nil {
		return foldResult{}, err
	}
	delete(raw, "layers")
	d.add(c.Path)
	if err := o.flatten("", raw, flat, d); err != nil {
		return foldResult{}, err
	}
	done := foldResult{keys: flat, deps: d.seen}
	memo[c.Name] = done
	o.folded.put(c.Name, flat, d.seen)
	return done, nil
}

// foldResult is a composed patch and the files it was composed from. The deps
// travel with the keys so a parent inherits them directly rather than reading
// them back out of the cache, which a concurrent invalidation could have
// emptied — and a parent missing a child's dependency is a stale patch.
type foldResult struct {
	keys map[string]json.RawMessage
	deps []dep
}

// foldedDeps returns the dependencies a cached fold was built from.
func (o *Outfitter) foldedDeps(name string) []dep {
	o.folded.mu.Lock()
	defer o.folded.mu.Unlock()
	return o.folded.entries[name].deps
}

// resolvePath finds an outfit file (outfits/<name>.toml). The
// legacy providers/<name>/config.toml fallback has been removed:
// provider directories now only carry auth credentials.
func (o *Outfitter) resolvePath(name string) (string, error) {
	// loadouts/ is the pre-rename directory, still read if it survived.
	canonical := filepath.Join(o.configDir, "outfits", name+".toml")
	for _, path := range []string{canonical, filepath.Join(o.configDir, "loadouts", name+".toml")} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("outfit: stat %s: %w", path, err)
		}
	}
	return "", &os.PathError{Op: "open", Path: canonical, Err: os.ErrNotExist}
}

// flatten walks a TOML tree into dotted chalkboard keys, expanding
// fileName/dirName single-key tables.
//
// `fileName` produces a content-envelope object at the table's key:
//
//	{ "frontmatter": "...", "filePath": "..." }   // if file begins with ---
//	{ "content":     "...", "filePath": "..." }   // otherwise
//
// `dirName` fans each file out as its own dotted key under the
// table — `skills = { dirName = "skills" }` yields `skills.<base>`
// entries, each carrying a full envelope. This shape lets completion
// pickers see each item individually rather than receiving one opaque
// JSON blob.
//
// `frontmatter` is the raw frontmatter text (between the fences),
// unparsed; the agent reads the file when it wants the body. When
// no frontmatter is present, the full body lands in `content`.
func (o *Outfitter) flatten(prefix string, in map[string]any, out map[string]json.RawMessage, d *deps) error {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			if fn, ok := val["fileName"].(string); ok && len(val) == 1 {
				path := filepath.Join(o.configDir, fn)
				body, err := os.ReadFile(path)
				d.add(path)
				if err != nil {
					return fmt.Errorf("outfit: %s fileName=%q: %w", key, fn, err)
				}
				env := contentEnvelope(string(body), path)
				b, err := json.Marshal(env)
				if err != nil {
					return err
				}
				out[key] = b
				continue
			}
			if dn, ok := val["dirName"].(string); ok && len(val) == 1 {
				// Bundled (first-party, shipped with the binary) skills load
				// first; the user's config dir loads second and overrides by
				// name. So a user can shadow a bundled skill, and first-party
				// skills appear without copying anything into config.
				m := map[string]ContentEnvelope{}
				if root := bundledSkillsRoot(); root != "" {
					b, err := loadDir(filepath.Join(root, dn), d)
					if err != nil {
						return fmt.Errorf("outfit: %s bundled dirName=%q: %w", key, dn, err)
					}
					for name, env := range b {
						m[name] = env
					}
				}
				u, err := loadDir(filepath.Join(o.configDir, dn), d)
				if err != nil {
					return fmt.Errorf("outfit: %s dirName=%q: %w", key, dn, err)
				}
				for name, env := range u {
					m[name] = env
				}
				for name, env := range m {
					b, err := json.Marshal(env)
					if err != nil {
						return err
					}
					out[key+"."+name] = b
				}
				continue
			}
			if err := o.flatten(key, val, out, d); err != nil {
				return err
			}
		default:
			b, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("outfit: marshal %s: %w", key, err)
			}
			out[key] = b
		}
	}
	return nil
}

// ContentEnvelope is the chalkboard shape for fileName/dirName-loaded
// content. Exactly one of Frontmatter or Content is non-empty: if the
// file began with a `---` fence, the raw frontmatter text goes in
// Frontmatter and the body is omitted; otherwise the full body goes
// in Content. FilePath is always set so the agent can read the body
// when it needs the elided text.
type ContentEnvelope struct {
	Frontmatter string `json:"frontmatter,omitempty"`
	Content     string `json:"content,omitempty"`
	FilePath    string `json:"filePath"`
}

// contentEnvelope builds an envelope: frontmatter-only if the body
// begins with a `---` fence and the close fence is found; full
// content otherwise.
func contentEnvelope(body, path string) ContentEnvelope {
	if fm, ok := extractFrontmatter(body); ok {
		return ContentEnvelope{Frontmatter: fm, FilePath: path}
	}
	return ContentEnvelope{Content: body, FilePath: path}
}

// extractFrontmatter returns the raw text between the opening and
// closing `---` fences, or ("", false) if no parseable frontmatter
// block is found. The body must begin with a `---` fence on its own
// line (BOM and leading whitespace are not tolerated — frontmatter is
// opt-in), but the line ending may be LF or CRLF.
//
// CRLF is not a nicety. The failure is SILENT AND EXPENSIVE: a skill whose
// fence is not recognised falls through to the full-body envelope, so the
// WHOLE FILE lands in the chalkboard and is inherited by every aria minted
// from that outfit. Six skills saved with Windows line endings put 101KB —
// roughly 25k tokens — into every new aria on this author's box, none of it
// asked for and none of it visible as anything but a large context.
func extractFrontmatter(body string) (string, bool) {
	rest, ok := strings.CutPrefix(body, "---\n")
	if !ok {
		if rest, ok = strings.CutPrefix(body, "---\r\n"); !ok {
			return "", false
		}
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	// Normalise the block itself, not just the closing fence. The
	// frontmatter string lands in the chalkboard verbatim and the outfit's
	// content version is a hash of that patch -- so a CRLF-saved skill and
	// an LF-saved copy of the SAME skill would otherwise mint two different
	// outfit stumps, with two shared prefixes and two caches, on two
	// machines editing one repository.
	return strings.ReplaceAll(strings.TrimSuffix(rest[:end], "\r"), "\r\n", "\n"), true
}

// loadDir reads file skills and directory skills from dir. A directory skill
// is keyed by its directory name and rooted at SKILL.md (or skill.md).
func loadDir(dir string, d *deps) (map[string]ContentEnvelope, error) {
	// The directory's own stat comes first: adding or removing a skill changes
	// it while leaving every surviving file untouched.
	d.add(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ContentEnvelope{}, nil
		}
		return nil, err
	}
	out := map[string]ContentEnvelope{}
	for _, e := range entries {
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = info.IsDir()
			} else {
				continue
			}
		}
		if isDir {
			skillPath := directorySkillPath(filepath.Join(dir, e.Name()))
			if skillPath == "" {
				continue
			}
			body, err := os.ReadFile(skillPath)
			d.add(skillPath)
			if err != nil {
				continue
			}
			out[e.Name()] = contentEnvelope(string(body), skillPath)
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		d.add(path)
		if err != nil {
			return nil, err
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		out[name] = contentEnvelope(string(body), path)
	}
	return out, nil
}

func directorySkillPath(dir string) string {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// bundledSkillsRoot returns the directory holding first-party skills shipped
// with the binary (its parent is <exe>/../share/figaro, so dirName="skills"
// resolves to <exe>/../share/figaro/skills). FIGARO_BUNDLED_SKILLS overrides:
// a path uses that root; "0"/"off"/"" disables bundled skills entirely.
func bundledSkillsRoot() string {
	if v, ok := os.LookupEnv("FIGARO_BUNDLED_SKILLS"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "0", "off", "false":
			return ""
		default:
			return v
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "share", "figaro")
}
