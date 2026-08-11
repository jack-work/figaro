// Package outfit assembles an aria's form from on-disk config.
//
// Load reads a named outfit TOML chain and returns a form patch.
// Providers read `system.credo` (and other system keys) straight off
// the form: no derivation step.
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

	"github.com/jack-work/figaro/internal/form"
)

// Outfitter assembles forms from on-disk outfits, and is the only thing in
// the daemon that reads an outfit file. Safe for concurrent use, and worth
// reusing: everything it reads is snapshotted and cached per EPOCH, so a
// repeat fold costs a map lookup rather than a syscall per dependency
// (resolver.go holds that machinery and the reasons for it).
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

// New returns an Outfitter rooted at configDir with NO snapshot store: reads
// come from the live files, epoch-cached. A library that writes to a user's
// disk without being asked is a library that surprises someone, so the
// snapshot store is opt-in, and the component that has a state directory and
// a lifecycle to hang it on is the daemon, which calls NewAt.
func New(configDir string) *Outfitter {
	return NewAt(configDir, "")
}

// NewAt is New with the snapshot store placed by hand: what the daemon uses
// (its own state directory) and what tests use (a temp dir). An empty snapDir
// disables snapshotting: reads then come straight from the live files, which
// is the pre-snapshot behaviour and still correct, just not edit-proof.
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

// Closure is one node of an outfit's layer graph: the outfit itself, the
// layers it declares, and whether each was actually found on disk. It is built
// even when layers are missing, because a broken reference is best explained by
// showing the shape it was found in.
type Closure struct {
	Name  string
	Path  string // "" when the outfit was not found
	Found bool
	Cycle bool // this name is already being resolved further up the chain
	// Err is a fault in the FILE itself: unparseable TOML, a malformed
	// layers list. It rides on the node rather than aborting the walk, so the
	// closure can still be drawn around the break, and closureError reports it
	// ahead of anything else because it is the most specific thing wrong.
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

// MissingError reports that something in the closure is not on disk. It
// carries the whole closure so a caller can show where the gap is rather than
// only name it.
type MissingError struct {
	Closure *Closure
	Missing []string
	// RootOnly is true when the only thing missing is the outfit that was
	// asked for. That is the ordinary "no such outfit" and callers may treat
	// it as an absence rather than a fault: the first-run flow does.
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

// Load resolves one named outfit and returns the form patch. A missing
// outfit is an error: someone naming one wants to hear that it does not exist.
func (o *Outfitter) Load(name string) (form.Patch, error) {
	return o.load(name, true)
}

// LoadOptional is Load, except that an outfit that does not exist yields an
// empty patch rather than an error. The first-run flow rides on that absence:
// config names a default that is not written yet, the patch comes back empty,
// and the missing system.provider is noticed downstream.
func (o *Outfitter) LoadOptional(name string) (form.Patch, error) {
	return o.load(name, false)
}

// load resolves one outfit's closure and folds it. A layer referenced by an
// outfit that DOES exist is a broken reference under either strictness, because
// the alternative is to discard the whole patch and let a typo look like an
// empty outfit.
func (o *Outfitter) load(name string, strict bool) (form.Patch, error) {
	if name == "" {
		return form.Patch{}, nil
	}
	if err := ValidName(name); err != nil {
		return form.Patch{}, err
	}
	ep := o.current()
	// The fast path, and the whole point of the epoch: a fold already
	// materialized in this view is returned without touching the disk or
	// re-proving anything about the graph.
	if keys, ok := o.getFold(ep.gen, name); ok {
		return form.Patch{Set: keys}, nil
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
	return form.Patch{Set: keys}, nil
}

// Resolve builds the closure for one named outfit. It reads each file's
// layer list (once per epoch, from the snapshot) and nothing else.
func (o *Outfitter) Resolve(name string) *Closure {
	return o.resolveIn(o.current(), name)
}

// resolveIn is Resolve inside a known epoch. stack is the chain currently
// being resolved, so a name that reappears on it is a cycle rather than a
// second visit; memo holds finished nodes, which by definition are not on the
// stack. Both the node parse and the cycle verdict are cached in the epoch,
// which is what "never sort more than you have to" comes to in practice: the
// memoised depth-first walk IS the topological sort, built only over the part
// of the graph anyone asked about.
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
		// The file is gone. Nothing downstream will consult its cached fold -
		// resolution stops here: so this is the only moment we learn it is
		// collectible, and the only place that can reap it.
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

// layerNames extracts and validates the layers key.
//
// `source` was the single-parent spelling this replaced. It is rejected rather
// than ignored: left alone it would flatten into a form key named
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
		if err := ValidName(name); err != nil {
			return nil, fmt.Errorf("outfit: %s: layers[%d]: %w", path, i, err)
		}
		out = append(out, name)
	}
	return out, nil
}

// closureError reports the first structural fault in a closure: a cycle, or
// anything not on disk. RootOnly says the only thing missing is the outfit that
// was asked for: the ordinary "no such outfit", which a lenient caller may
// treat as an absence.
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

// foldIn returns one outfit's flattened keys: its layers in order, then its
// own, so the nearest declaration wins. Cached per name per epoch, which makes
// a layer shared by several others cheap at each of its positions, and, since
// an epoch is a consistent view, costs nothing to validate.
//
// stack carries the chain being folded. A name that reappears on it is a
// cycle: the loop is named in the error and every name ON it is TAINTED, so a
// second ask answers from the taint instead of walking the graph again. The
// taint dies with the epoch.
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

// taintOf answers instantly for a name already known to sit inside a cycle.
func (o *Outfitter) taintOf(ep *epoch, name string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ep.taints[name]
}

// taint marks every name on a cycle, so none of them is ever walked again in
// this epoch: not the one that closed the loop, and not the ones that only
// lead into it.
func (o *Outfitter) taint(ep *epoch, names []string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, n := range names {
		ep.taints[n] = err
	}
}

// decodeTOML parses bytes that came from a snapshot rather than a path, so
// what is parsed is exactly what was pinned.
func decodeTOML(b []byte, path string) (map[string]any, error) {
	raw := map[string]any{}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("outfit: parse %s: %w", path, err)
	}
	return raw, nil
}

// resolvePath finds an outfit file (outfits/<name>.toml). The
// legacy providers/<name>/config.toml fallback has been removed:
// provider directories now only carry auth credentials.
func (o *Outfitter) resolvePath(name string, r *reader) (string, error) {
	// loadouts/ is the pre-rename directory, still read if it survived.
	canonical := filepath.Join(o.configDir, "outfits", name+".toml")
	for _, path := range []string{canonical, filepath.Join(o.configDir, "loadouts", name+".toml")} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("outfit: stat %s: %w", path, err)
		}
	}
	// A name that resolved to NOTHING is still a fact about the disk: record
	// the path that was missing, so creating the file later turns the epoch
	// over instead of being invisible behind a cached absence.
	if r != nil {
		r.stat(canonical)
	}
	return "", &os.PathError{Op: "open", Path: canonical, Err: os.ErrNotExist}
}

// flatten walks a TOML tree into dotted form keys, expanding
// fileName/dirName single-key tables.
//
// `fileName` produces a content-envelope object at the table's key:
//
//	{ "frontmatter": "...", "filePath": "..." }   // if file begins with ---
//	{ "content":     "...", "filePath": "..." }   // otherwise
//
// `dirName` fans each file out as its own dotted key under the
// table: `skills = { dirName = "skills" }` yields `skills.<base>`
// entries, each carrying a full envelope. This shape lets completion
// pickers see each item individually rather than receiving one opaque
// JSON blob.
//
// `frontmatter` is the raw frontmatter text (between the fences),
// unparsed; the agent reads the file when it wants the body. When
// no frontmatter is present, the full body lands in `content`.
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
				env := contentEnvelope(string(body), path)
				b, err := json.Marshal(env)
				if err != nil {
					return err
				}
				out[key] = b
				continue
			}
			if dn, ok := val["dirName"].(string); ok && len(val) == 1 {
				// The user's config dir loads first; BUNDLED (first-party,
				// shipped with the binary) skills load second and win by name.
				//
				// That order used to be the other way round, and it was a trap
				// with no alarm on it. A copy in ~/.config outranked the
				// shipped skill FOREVER: an upgrade could not reach it, so the
				// copy silently fell behind the binary it documented: one such
				// shadow in this repo's history ended up 201 lines stale while
				// holding the only copy of a section that had moved. A skill
				// that ships with figaro is part of figaro, and an install must
				// be able to correct it.
				//
				// A name the binary does not ship is untouched, so a user's own
				// skills are safe; to override a bundled one, give it a
				// different name.
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
					b, err := json.Marshal(env)
					if err != nil {
						return err
					}
					out[key+"."+name] = b
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

// assetPath resolves a fileName/dirName reference against the root it is
// allowed to read, and refuses anything that leaves it.
//
// The reference is DATA. It arrives from an outfit file today, and an outfit
// file is the user's own: but the loader is the thing that turns a string into
// a file read inside the daemon, and the daemon reads with the daemon's
// privileges on the daemon's filesystem. Before the spec collapse a client could
// send `-O '{"x":{"fileName":"../../.ssh/id_ed25519"}}'` and have the contents
// folded onto its own form and rendered to a provider: a confused deputy with a
// one-flag trigger. That path is gone (a literal's keys never reach the loader
// now), and this makes sure it cannot come back by another door.
//
// Symlinks are followed and then checked, so a link inside the root pointing
// out of it is refused too: that is the version of this bug that survives a
// naive prefix test.
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
		// Not there yet (or a broken link): the caller reports the open error,
		// which is the more useful message. The clean-and-rejoin above already
		// removed any `..`, so nothing outside root can be named here.
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

// ContentEnvelope is the form shape for fileName/dirName-loaded
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
// line (BOM and leading whitespace are not tolerated: frontmatter is
// opt-in), but the line ending may be LF or CRLF.
//
// CRLF is not a nicety. The failure is SILENT AND EXPENSIVE: a skill whose
// fence is not recognised falls through to the full-body envelope, so the
// WHOLE FILE lands in the form and is inherited by every aria minted
// from that outfit. Six skills saved with Windows line endings put 101KB -
// roughly 25k tokens: into every new aria on this author's box, none of it
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
	// frontmatter string lands in the form verbatim and the outfit's
	// content version is a hash of that patch -- so a CRLF-saved skill and
	// an LF-saved copy of the SAME skill would otherwise mint two different
	// outfit stumps, with two shared prefixes and two caches, on two
	// machines editing one repository.
	return strings.ReplaceAll(strings.TrimSuffix(rest[:end], "\r"), "\r\n", "\n"), true
}

// loadDir reads file skills and directory skills from dir. A directory skill
// is keyed by its directory name and rooted at SKILL.md (or skill.md).
func loadDir(dir string, r *reader) (map[string]ContentEnvelope, error) {
	// The directory's own stat comes first: adding or removing a skill changes
	// it while leaving every surviving file untouched.
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
		path := filepath.Join(dir, e.Name())
		body, err := r.read(path)
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

// The bundled-skills root lives in bundled.go: the skills ride inside the
// binary and are unpacked to a content-hashed directory on first use.
