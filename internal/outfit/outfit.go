// Package outfit assembles an aria's chalkboard from on-disk config.
//
// Load reads a named loadout TOML chain and returns a chalkboard patch.
// Bootstrap templates system.credo into system.prompt.
package outfit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"

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

// BootCtx carries identity fields exposed to the credo template.
type BootCtx struct {
	Provider string
	FigaroID string
	Version  string
}

// CurrentBootCtx fills BootCtx with runtime values.
func CurrentBootCtx(provider, figaroID string) BootCtx {
	return BootCtx{
		Provider: provider,
		FigaroID: figaroID,
		Version:  buildVersion(),
	}
}

// Load resolves a loadout and returns the chalkboard patch.
func (o *Outfitter) Load(name string) (chalkboard.Patch, error) {
	if name == "" {
		name = "config"
	}
	flat := map[string]json.RawMessage{}
	visited := map[string]bool{}
	if err := o.loadInto(name, flat, visited); err != nil {
		return chalkboard.Patch{}, err
	}
	return chalkboard.Patch{Set: flat}, nil
}

// Bootstrap templates system.credo -> system.prompt. No-op when
// system.prompt is already set. Skills are no longer touched here —
// the loader writes them directly via the dirName form.
func (o *Outfitter) Bootstrap(snap chalkboard.Snapshot, ctx BootCtx) (chalkboard.Patch, error) {
	if _, ok := snap["system.prompt"]; ok {
		return chalkboard.Patch{}, nil
	}

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}

	// Render credo template into system.prompt. system.credo arrives
	// from the loader as a ContentEnvelope; read its content (or
	// frontmatter, if that's all the user gave us). Older configs that
	// stored a bare string still work via the fallback.
	var credoBody string
	if raw, ok := snap["system.credo"]; ok {
		var env ContentEnvelope
		if json.Unmarshal(raw, &env) == nil && (env.Content != "" || env.Frontmatter != "") {
			credoBody = env.Content
			if credoBody == "" {
				credoBody = env.Frontmatter
			}
		} else {
			_ = json.Unmarshal(raw, &credoBody)
		}
	}
	if credoBody != "" {
		tmpl, err := template.New("credo").Parse(credoBody)
		if err != nil {
			return chalkboard.Patch{}, fmt.Errorf("outfit: parse credo template: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, ctx); err != nil {
			return chalkboard.Patch{}, fmt.Errorf("outfit: execute credo template: %w", err)
		}
		b, _ := json.Marshal(buf.String())
		patch.Set["system.prompt"] = b
	}

	return patch, nil
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

// resolvePath finds a loadout file (loadouts/ then providers/).
func (o *Outfitter) resolvePath(name string) (string, error) {
	candidates := []string{
		filepath.Join(o.configDir, "loadouts", name+".toml"),
		filepath.Join(o.configDir, "providers", name, "config.toml"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("outfit: loadout %q not found (checked %s)", name, strings.Join(candidates, ", "))
}

// flatten walks a TOML tree into dotted chalkboard keys, expanding
// fileName/dirName single-key tables.
//
// Both `fileName` and `dirName` produce content-envelope objects:
//
//	{ "frontmatter": "...", "filePath": "..." }   // if file begins with ---
//	{ "content":     "...", "filePath": "..." }   // otherwise
//
// `frontmatter` is the raw frontmatter text (between the fences),
// unparsed; the agent reads the file when it wants the body. When
// no frontmatter is present, the full body lands in `content`.
// `dirName` produces a map basename->envelope; `fileName` produces a
// single envelope.
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
				m, err := loadDir(filepath.Join(o.configDir, dn))
				if err != nil {
					return fmt.Errorf("outfit: %s dirName=%q: %w", key, dn, err)
				}
				b, err := json.Marshal(m)
				if err != nil {
					return err
				}
				out[key] = b
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
		if e.IsDir() {
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

// version can be set at link time.
var version string

// buildVersion extracts the VCS revision from build info.
func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 8 {
		rev = rev[:8]
	}
	return rev + dirty
}
