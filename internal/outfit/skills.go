package outfit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillCatalogEntry is the wire shape stored at chalkboard
// system.skills — the model-facing catalog. Bodies are deliberately
// omitted; the model loads a skill via the read tool when the task
// matches its description.
type SkillCatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"file_path"`
}

// FormatSkillCatalog renders a catalog as the markdown-shaped system
// block the provider emits.
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

// loadSkillCatalog reads .md files from dir, parses YAML-ish
// frontmatter for name + description, and returns the model-facing
// catalog. Missing dir → empty catalog, no error.
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

// parseFrontmatter extracts name + description from YAML-style
// frontmatter:
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
