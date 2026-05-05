// Package outfit (working title "loadout") loads a TOML config file
// describing an aria's initial chalkboard, resolves its `source` chain,
// expands fileName / dirName references, and produces a chalkboard
// Patch ready to seed a fresh aria.
//
// Lookup order for a name:
//
//	<configDir>/loadouts/<name>.toml
//	<configDir>/providers/<name>/config.toml
//
// "All providers conform to loadouts" — a provider's config.toml is a
// loadout that happens to define provider-specific defaults.
//
// File / directory references are inline tables with a single key:
//
//	credo  = { fileName = "credo.md"  }   # → loaded as a string
//	skills = { dirName  = "skills"    }   # → loaded as { <basename>: <body> }
//
// Paths are relative to configDir. Other inline tables are flattened
// into dotted chalkboard keys: `system = { model = "x" }` becomes
// `system.model = "x"`.
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

// Load resolves the named loadout, applies the source chain (parents
// first, child overlays on top), and returns the resulting chalkboard
// Patch. Empty name resolves to "config" (the user's top-level
// loadouts/config.toml).
func Load(configDir, name string) (chalkboard.Patch, error) {
	if name == "" {
		name = "config"
	}
	flat := map[string]json.RawMessage{}
	visited := map[string]bool{}
	if err := loadInto(configDir, name, flat, visited); err != nil {
		return chalkboard.Patch{}, err
	}
	return chalkboard.Patch{Set: flat}, nil
}

// loadInto resolves <name>, recursively visiting `source` first, then
// overlays the current loadout's values onto flat.
func loadInto(configDir, name string, flat map[string]json.RawMessage, visited map[string]bool) error {
	if visited[name] {
		return fmt.Errorf("outfit: cycle in source chain at %q", name)
	}
	visited[name] = true

	path, err := resolvePath(configDir, name)
	if err != nil {
		return err
	}
	raw := map[string]any{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return fmt.Errorf("outfit: parse %s: %w", path, err)
	}

	if src, ok := raw["source"].(string); ok && src != "" {
		if err := loadInto(configDir, src, flat, visited); err != nil {
			return err
		}
	}
	delete(raw, "source")

	return flatten(configDir, "", raw, flat)
}

// resolvePath looks up a loadout file by name. Tries loadouts/ first,
// then providers/.
func resolvePath(configDir, name string) (string, error) {
	candidates := []string{
		filepath.Join(configDir, "loadouts", name+".toml"),
		filepath.Join(configDir, "providers", name, "config.toml"),
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
func flatten(configDir, prefix string, in map[string]any, out map[string]json.RawMessage) error {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			if fn, ok := val["fileName"].(string); ok && len(val) == 1 {
				body, err := os.ReadFile(filepath.Join(configDir, fn))
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
				m, err := loadDir(filepath.Join(configDir, dn))
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
			if err := flatten(configDir, key, val, out); err != nil {
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
