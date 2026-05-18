package outfit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillCatalogEntry is the model-facing skill catalog entry as
// returned by the on-disk loader. Bodies are omitted; the model
// loads them via read.
type SkillCatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"file_path"`
}

// SkillEntry is the chalkboard shape stored under
// `system.skills.<name>` — name is implicit in the key.
type SkillEntry struct {
	Description string `json:"description"`
	FilePath    string `json:"file_path"`
}

// FormatSkillCatalog renders a catalog as a markdown system block.
func FormatSkillCatalog(entries []SkillCatalogEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Available Skills\n\n")
	b.WriteString("Use the read tool to load a skill's file when the task matches its description.\n\n")
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("## %s\n", e.Name))
		if e.Description != "" {
			b.WriteString(fmt.Sprintf("*%s*\n", e.Description))
		}
		b.WriteString(fmt.Sprintf("File: `%s`\n\n", e.FilePath))
	}
	return b.String()
}

// SkillsFromSnapshot collects `system.skills.<name>` keys out of
// snap into a deterministic catalog. Returns nil if none present.
func SkillsFromSnapshot(snap map[string]json.RawMessage) []SkillCatalogEntry {
	const prefix = "system.skills."
	var names []string
	for k := range snap {
		if strings.HasPrefix(k, prefix) && len(k) > len(prefix) {
			names = append(names, k[len(prefix):])
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := make([]SkillCatalogEntry, 0, len(names))
	for _, name := range names {
		var e SkillEntry
		if err := json.Unmarshal(snap[prefix+name], &e); err != nil {
			continue
		}
		out = append(out, SkillCatalogEntry{
			Name:        name,
			Description: e.Description,
			FilePath:    e.FilePath,
		})
	}
	return out
}

// loadSkillCatalog reads .md files with frontmatter from dir.
func loadSkillCatalog(dir string) ([]SkillCatalogEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SkillCatalogEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name, desc := parseFrontmatter(string(data))
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		out = append(out, SkillCatalogEntry{Name: name, Description: desc, FilePath: path})
	}
	return out, nil
}

// parseFrontmatter extracts name + description from:
//
//	---
//	name: foo
//	description: does bar
//	---
func parseFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", ""
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(content[4:4+end], "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "name":
			name = val
		case "description":
			description = val
		}
	}
	return
}
