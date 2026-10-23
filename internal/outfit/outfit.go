// Package outfit assembles an aria's form from on-disk config.
package outfit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/form"
)

// Outfitter is safe for concurrent use; reads are snapshotted and cached per epoch.
type Outfitter struct {
	configDir string
	snaps     *snapStore

	mu          sync.Mutex
	ep          *epoch
	folds       map[string]foldEntry
	foldBytes   int
	budget      int
	staleWindow time.Duration
}

// New has no snapshot store: reads come from the live files, epoch-cached.
func New(configDir string) *Outfitter {
	return NewAt(configDir, "")
}

// NewAt places the snapshot store at snapDir; empty disables snapshotting.
func NewAt(configDir, snapDir string) *Outfitter {
	return &Outfitter{
		configDir:   configDir,
		snaps:       newSnapStore(snapDir),
		folds:       map[string]foldEntry{},
		budget:      foldBudget,
		staleWindow: defaultStaleWindow,
		ep: &epoch{
			gen:     1,
			nodes:   map[string]*node{},
			taints:  map[string]error{},
			touched: map[string]dep{},
			checked: time.Now(),
		},
	}
}

// Closure is one node of an outfit's layer graph, built even around missing
// or broken layers.
type Closure struct {
	Name  string
	Path  string
	Found bool
	Cycle bool
	// Err is a fault in the file itself; it rides on the node so the walk continues.
	Err    error
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

type MissingError struct {
	Closure *Closure
	Missing []string
	// RootOnly: only the outfit asked for is missing, which callers may treat as absence.
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

func (o *Outfitter) Load(name string) (form.Patch, error) {
	return o.load(name, true)
}

// LoadOptional yields an empty patch for an outfit that does not exist.
func (o *Outfitter) LoadOptional(name string) (form.Patch, error) {
	return o.load(name, false)
}

// A missing layer of an existing outfit is an error under either strictness.
func (o *Outfitter) load(name string, strict bool) (form.Patch, error) {
	if name == "" {
		return form.Patch{}, nil
	}
	if err := ValidName(name); err != nil {
		return form.Patch{}, err
	}
	ep := o.current()
	if keys, ok := o.getFold(ep.gen, name); ok {
		return form.Build(form.Snapshot{}, keys, nil), nil
	}
	root := o.resolveIn(ep, name)
	if err := closureError(root); err != nil {
		var missing *MissingError
		if !strict && errors.As(err, &missing) && missing.RootOnly {
			return form.Patch{}, nil
		}
		return form.Patch{}, err
	}
	keys, err := o.foldIn(ep, name, nil)
	if err != nil {
		return form.Patch{}, err
	}
	return form.Build(form.Snapshot{}, keys, nil), nil
}

// Resolve reads only each file's layer list.
func (o *Outfitter) Resolve(name string) *Closure {
	return o.resolveIn(o.current(), name)
}

func (o *Outfitter) resolveIn(ep *epoch, name string) *Closure {
	return o.resolveNode(ep, name, nil, map[string]*Closure{})
}

func (o *Outfitter) resolveNode(ep *epoch, name string, stack []string, memo map[string]*Closure) *Closure {
	for _, s := range stack {
		if s == name {
			return &Closure{Name: name, Found: true, Cycle: true}
		}
	}
	if c, ok := memo[name]; ok {
		return c
	}
	n, err := o.nodeFor(ep, name)
	if err != nil {
		c := &Closure{Name: name, Found: true, Err: err}
		memo[name] = c
		return c
	}
	if !n.found {
		c := &Closure{Name: name}
		// The only place a vanished file's cached fold is learned to be collectible.
		o.Forget(name)
		memo[name] = c
		return c
	}
	c := &Closure{Name: name, Path: n.path, Found: true}
	for _, l := range n.layers {
		c.Layers = append(c.Layers, o.resolveNode(ep, l, append(stack, name), memo))
	}
	memo[name] = c
	return c
}

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
		if err := ValidName(name); err != nil {
			return nil, fmt.Errorf("outfit: %s: layers[%d]: %w", path, i, err)
		}
		out = append(out, name)
	}
	return out, nil
}

// closureError reports the most specific fault: a file error, then a cycle, then what is missing.
func closureError(root *Closure) error {
	var cycle string
	var missing []string
	var fault error
	root.Walk(func(c *Closure) {
		if c.Err != nil && fault == nil {
			fault = c.Err
		}
		if c.Cycle && cycle == "" {
			cycle = c.Name
		}
		if !c.Found {
			missing = append(missing, c.Name)
		}
	})
	if fault != nil {
		return fault
	}
	if cycle != "" {
		return &CycleError{Closure: root, At: cycle}
	}
	if len(missing) == 0 {
		return nil
	}
	return &MissingError{Closure: root, Missing: missing,
		RootOnly: len(missing) == 1 && missing[0] == root.Name}
}

// foldIn flattens layers in order, then the outfit's own keys: the nearest declaration wins.
func (o *Outfitter) foldIn(ep *epoch, name string, stack []string) (map[string]json.RawMessage, error) {
	if err := o.taintOf(ep, name); err != nil {
		return nil, err
	}
	for i, s := range stack {
		if s == name {
			err := &CycleError{At: name}
			o.taint(ep, append(stack[i:], name), err)
			return nil, err
		}
	}
	if keys, ok := o.getFold(ep.gen, name); ok {
		return keys, nil
	}
	n, err := o.nodeFor(ep, name)
	if err != nil {
		return nil, err
	}
	if !n.found {
		return nil, &MissingError{Missing: []string{name}, RootOnly: len(stack) == 0}
	}
	flat := map[string]json.RawMessage{}
	for _, layer := range n.layers {
		sub, serr := o.foldIn(ep, layer, append(stack, name))
		if serr != nil {
			return nil, serr
		}
		for k, v := range sub {
			flat[k] = v
		}
	}
	r := &reader{o: o, ep: ep}
	b, rerr := r.read(n.path)
	if rerr != nil {
		return nil, fmt.Errorf("outfit: read %s: %w", n.path, rerr)
	}
	raw, perr := decodeTOML(b, n.path)
	if perr != nil {
		return nil, perr
	}
	delete(raw, "layers")
	if err := o.flatten("", raw, flat, r); err != nil {
		return nil, err
	}
	o.putFold(ep.gen, name, flat)
	return flat, nil
}

func (o *Outfitter) taintOf(ep *epoch, name string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ep.taints[name]
}

// taint marks every name on a cycle so none is walked again this epoch.
func (o *Outfitter) taint(ep *epoch, names []string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, n := range names {
		ep.taints[n] = err
	}
}

func decodeTOML(b []byte, path string) (map[string]any, error) {
	raw := map[string]any{}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("outfit: parse %s: %w", path, err)
	}
	return raw, nil
}

func (o *Outfitter) resolvePath(name string, r *reader) (string, error) {
	var files map[string][]string
	var err error
	if r != nil {
		files, err = r.outfitIndex(name)
	} else {
		files, err = walkOutfits(o.configDir, nil)
	}
	if err != nil {
		return "", fmt.Errorf("outfit: %w", err)
	}
	switch paths := files[name]; len(paths) {
	case 1:
		return paths[0], nil
	case 0:
		canonical := filepath.Join(o.configDir, "outfits", name+".toml")
		if r != nil {
			r.stat(canonical)
		}
		return "", &os.PathError{Op: "open", Path: canonical, Err: os.ErrNotExist}
	default:
		rel := make([]string, len(paths))
		for i, p := range paths {
			rel[i] = p
			if r, err := filepath.Rel(o.configDir, p); err == nil {
				rel[i] = r
			}
		}
		return "", fmt.Errorf("outfit %q is ambiguous: %s; an outfit is named by its file, so rename one", name, strings.Join(rel, " and "))
	}
}

// flatten walks a TOML tree into dotted form keys, expanding fileName/dirName single-key tables.
func (o *Outfitter) flatten(prefix string, in map[string]any, out map[string]json.RawMessage, r *reader) error {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			if fn, ok := val["fileName"].(string); ok && len(val) == 1 {
				path, perr := assetPath(o.configDir, fn)
				if perr != nil {
					return fmt.Errorf("outfit: %s fileName=%q: %w", key, fn, perr)
				}
				body, err := r.read(path)
				if err != nil {
					return fmt.Errorf("outfit: %s fileName=%q: %w", key, fn, err)
				}
				emitEnvelope(out, key, contentEnvelope(string(body), path))
				continue
			}
			if dn, ok := val["dirName"].(string); ok && len(val) == 1 {
				// Bundled skills load after the config dir and win by name.
				m := map[string]ContentEnvelope{}
				udir, uerr := assetPath(o.configDir, dn)
				if uerr != nil {
					return fmt.Errorf("outfit: %s dirName=%q: %w", key, dn, uerr)
				}
				u, err := loadDir(udir, r)
				if err != nil {
					return fmt.Errorf("outfit: %s dirName=%q: %w", key, dn, err)
				}
				for name, env := range u {
					m[name] = env
				}
				if root := bundledSkillsRoot(); root != "" {
					bdir, berr := assetPath(root, dn)
					if berr != nil {
						return fmt.Errorf("outfit: %s bundled dirName=%q: %w", key, dn, berr)
					}
					b, err := loadDir(bdir, r)
					if err != nil {
						return fmt.Errorf("outfit: %s bundled dirName=%q: %w", key, dn, err)
					}
					for name, env := range b {
						m[name] = env
					}
				}
				for name, env := range m {
					emitEnvelope(out, key+"."+name, env)
				}
				continue
			}
			if err := o.flatten(key, val, out, r); err != nil {
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

// assetPath refuses a reference that resolves outside root, symlinks included.
func assetPath(root, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("%q must be relative to the config directory", ref)
	}
	joined := filepath.Join(root, filepath.Clean("/"+ref))
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// Missing or broken link: the clean-and-rejoin already removed any `..`.
		return joined, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q resolves outside %s", ref, realRoot)
	}
	return joined, nil
}

// ContentEnvelope carries exactly one of Frontmatter or Content; FilePath is always set.
type ContentEnvelope struct {
	Frontmatter string `json:"frontmatter,omitempty"`
	Content     string `json:"content,omitempty"`
	FilePath    string `json:"filePath"`
}

func contentEnvelope(body, path string) ContentEnvelope {
	if fm, ok := extractFrontmatter(body); ok {
		return ContentEnvelope{Frontmatter: fm, FilePath: path}
	}
	return ContentEnvelope{Content: body, FilePath: path}
}

// The body must begin with a `---` fence on its own line (no BOM or leading
// whitespace); the line ending may be LF or CRLF.
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
	// Normalised: the content version hashes this text, so CRLF and LF copies must agree.
	return strings.ReplaceAll(strings.TrimSuffix(rest[:end], "\r"), "\r\n", "\n"), true
}

// A directory skill is keyed by its directory name and rooted at SKILL.md (or skill.md).
func loadDir(dir string, r *reader) (map[string]ContentEnvelope, error) {
	entries, err := r.readDir(dir)
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
			body, err := r.read(skillPath)
			if err != nil {
				continue
			}
			out[e.Name()] = contentEnvelope(string(body), skillPath)
			continue
		}
		name, ok := skillName(e.Name())
		if !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := r.read(path)
		if err != nil {
			return nil, err
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

// Only a .md file is a skill; the name is everything before that suffix, dots and all.
func skillName(file string) (string, bool) {
	const ext = ".md"
	if !strings.HasSuffix(file, ext) {
		return "", false
	}
	name := strings.TrimSuffix(file, ext)
	if name == "" || strings.HasPrefix(name, ".") {
		return "", false
	}
	return name, true
}

// One key per field, so a form can reach into an envelope.
func emitEnvelope(out map[string]json.RawMessage, prefix string, env ContentEnvelope) {
	put := func(field, v string) {
		if v == "" {
			return
		}
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		out[prefix+"."+field] = b
	}
	put("frontmatter", env.Frontmatter)
	put("content", env.Content)
	put("filePath", env.FilePath)
}
