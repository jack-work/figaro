// Package outfit assembles an aria's chalkboard from on-disk config.
//
// Load reads a named loadout TOML chain and returns a chalkboard patch.
// Providers read `system.credo` (and other system keys) straight off
// the chalkboard — no derivation step.
package outfit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Outfitter assembles chalkboards from on-disk loadouts.
type Outfitter struct {
	configDir string
}

// New returns an Outfitter rooted at configDir.
func New(configDir string) *Outfitter {
	return &Outfitter{configDir: configDir}
}

// Load resolves a loadout and returns the chalkboard patch.
//
// A missing loadout file is NOT an error: an empty patch is
// returned. The caller decides whether the resulting absence of
// system.provider is fatal. Parse errors and source-chain cycles
// still bubble up.
func (o *Outfitter) Load(name string) (chalkboard.Patch, error) {
	if name == "" {
		return chalkboard.Patch{}, nil
	}
	flat := map[string]json.RawMessage{}
	visited := map[string]bool{}
	if err := o.loadInto(name, flat, visited); err != nil {
		if os.IsNotExist(err) {
			return chalkboard.Patch{}, nil
		}
		return chalkboard.Patch{}, err
	}
	return chalkboard.Patch{Set: flat}, nil
}

// loadInto resolves a loadout recursively via source chains.
func (o *Outfitter) loadInto(name string, flat map[string]json.RawMessage, visited map[string]bool) error {
	if visited[name] {
		return fmt.Errorf("outfit: cycle in source chain at %q", name)
	}
	visited[name] = true

	path, err := o.resolvePath(name)
	if err != nil {
		return err
	}
	raw := map[string]any{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return fmt.Errorf("outfit: parse %s: %w", path, err)
	}

	if src, ok := raw["source"].(string); ok && src != "" {
		if err := o.loadInto(src, flat, visited); err != nil {
			return err
		}
	}
	delete(raw, "source")

	return o.flatten("", raw, flat)
}

// resolvePath finds a loadout file (loadouts/<name>.toml). The
// legacy providers/<name>/config.toml fallback has been removed:
// provider directories now only carry auth credentials.
func (o *Outfitter) resolvePath(name string) (string, error) {
	path := filepath.Join(o.configDir, "loadouts", name+".toml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("outfit: stat %s: %w", path, err)
	}
	return "", &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
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
func (o *Outfitter) flatten(prefix string, in map[string]any, out map[string]json.RawMessage) error {
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
					b, err := loadDir(filepath.Join(root, dn))
					if err != nil {
						return fmt.Errorf("outfit: %s bundled dirName=%q: %w", key, dn, err)
					}
					for name, env := range b {
						m[name] = env
					}
				}
				u, err := loadDir(filepath.Join(o.configDir, dn))
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
			if err := o.flatten(key, val, out); err != nil {
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
// block is found. The body must begin with `---\n` (BOM and leading
// whitespace are not tolerated — frontmatter is opt-in).
func extractFrontmatter(body string) (string, bool) {
	if !strings.HasPrefix(body, "---\n") {
		return "", false
	}
	rest := body[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// loadDir reads files from dir into a basename->ContentEnvelope map.
// Basenames drop their extension. Subdirectories are skipped.
func loadDir(dir string) (map[string]ContentEnvelope, error) {
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
			// A subdirectory holding a SKILL.md is ONE skill keyed by the
			// directory name; its other files are sections the agent reads on
			// demand via paths referenced from SKILL.md. Subdirs without a
			// SKILL.md are ignored.
			skillPath := filepath.Join(dir, e.Name(), "SKILL.md")
			body, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			out[e.Name()] = contentEnvelope(string(body), skillPath)
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
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
