// Package outfit assembles an aria's chalkboard from on-disk
// configuration. The Outfitter owns two phases:
//
//  1. Load — read a named loadout TOML, follow its `source` chain,
//     flatten inline tables to dotted chalkboard keys, expand
//     `{ fileName = … }` and `{ dirName = … }` single-key tables as
//     inlined contents. Returns the patch the caller applies before
//     constructing the agent.
//
//  2. Bootstrap — second-phase outfitting on a populated chalkboard:
//     templates `system.credo` (raw body) into `system.prompt`, parses
//     the skills directory into a structured catalog at
//     `system.skills`. Idempotent; skipped when system.prompt is
//     already set.
//
// Loadout lookup tries `<configDir>/loadouts/<name>.toml` first, then
// falls back to `<configDir>/providers/<name>/config.toml` — providers
// are loadouts, just stored under a sibling directory.
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

// Outfitter assembles chalkboards from on-disk loadouts. One instance
// per angelus is sufficient — the configDir is the only state.
type Outfitter struct {
	configDir string
}

// New returns an Outfitter rooted at configDir (typically
// ~/.config/figaro).
func New(configDir string) *Outfitter {
	return &Outfitter{configDir: configDir}
}

// BootCtx carries identity fields exposed to the credo template.
type BootCtx struct {
	Provider string
	FigaroID string
	Version  string
}

// CurrentBootCtx fills BootCtx with the runtime values an agent
// knows about: provider name, figaro id, and the build's VCS rev.
func CurrentBootCtx(provider, figaroID string) BootCtx {
	return BootCtx{
		Provider: provider,
		FigaroID: figaroID,
		Version:  buildVersion(),
	}
}

// Load resolves the named loadout and returns the chalkboard patch
// to apply. Empty name resolves to "config".
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

// Bootstrap is the second-phase outfitting. It reads the
// loadout-populated snapshot and produces the patch that finishes
// dressing the aria — templates the credo into system.prompt and
// parses the skills directory into system.skills. Returns an empty
// patch when system.prompt is already set (restored arias).
func (o *Outfitter) Bootstrap(snap chalkboard.Snapshot, ctx BootCtx) (chalkboard.Patch, error) {
	if _, ok := snap["system.prompt"]; ok {
		return chalkboard.Patch{}, nil
	}

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}

	// Credo: take the raw template body that the loadout's
	// fileName-resolution wrote into system.credo, render it.
	var credoBody string
	if raw, ok := snap["system.credo"]; ok {
		_ = json.Unmarshal(raw, &credoBody)
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

	// Skills: read the configured skills directory off disk so we
	// have absolute paths to expose to the model. The loadout's
	// generic dirName-loaded value (basename → body map) is
	// overwritten with the structured catalog the providers consume.
	skillsDir := filepath.Join(o.configDir, "skills")
	if catalog, err := loadSkillCatalog(skillsDir); err == nil && len(catalog) > 0 {
		if b, mErr := json.Marshal(catalog); mErr == nil {
			patch.Set["system.skills"] = b
		}
	}

	return patch, nil
}

// loadInto resolves <name>, recursively visiting `source` first, then
// overlays the current loadout's values onto flat.
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

// resolvePath looks up a loadout file by name. Tries loadouts/ first,
// then providers/.
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

// flatten walks a parsed TOML tree and writes dotted-key entries into
// out. Inline tables with a single fileName / dirName key are
// expanded: fileName → file body as JSON string, dirName → JSON
// object keyed by file basename.
func (o *Outfitter) flatten(prefix string, in map[string]any, out map[string]json.RawMessage) error {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			if fn, ok := val["fileName"].(string); ok && len(val) == 1 {
				body, err := os.ReadFile(filepath.Join(o.configDir, fn))
				if err != nil {
					return fmt.Errorf("outfit: %s fileName=%q: %w", key, fn, err)
				}
				b, err := json.Marshal(string(body))
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

// loadDir reads regular files from dir and returns a map of basename
// (without extension) → file body. Subdirectories are skipped.
// Missing dir is not an error — returns an empty map.
func loadDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		out[name] = string(body)
	}
	return out, nil
}

// version can be set at link time via:
//
//	-X github.com/jack-work/figaro/internal/outfit.version=abc1234
var version string

// buildVersion extracts the VCS revision from Go's embedded build info.
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
