// Package outfit assembles an aria's chalkboard from on-disk config.
//
// Load reads a named loadout TOML chain and returns a chalkboard patch.
// Bootstrap templates system.credo into system.prompt and parses skills.
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

// Bootstrap templates system.credo -> system.prompt and parses
// skills. No-op when system.prompt is already set.
func (o *Outfitter) Bootstrap(snap chalkboard.Snapshot, ctx BootCtx) (chalkboard.Patch, error) {
	if _, ok := snap["system.prompt"]; ok {
		return chalkboard.Patch{}, nil
	}

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}

	// Render credo template into system.prompt.
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

	// Parse skills directory into structured catalog.
	skillsDir := filepath.Join(o.configDir, "skills")
	if catalog, err := loadSkillCatalog(skillsDir); err == nil && len(catalog) > 0 {
		if b, mErr := json.Marshal(catalog); mErr == nil {
			patch.Set["system.skills"] = b
		}
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

// loadDir reads files from dir into a basename->body map.
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
