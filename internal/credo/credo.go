// Package credo constructs the system prompt (the "credo") for figaro agents.
//
// The credo is assembled from:
//  1. A template file (credo.md) with Go text/template fields
//  2. Runtime values (cwd, datetime, model, etc.)
//  3. Skills loaded from the config directory
//
// The CredoScribe interface allows swapping the assembly strategy.
// The DefaultScribe reads credo.md from the config directory and
// loads skills from ~/.config/figaro/skills/.
package credo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"
	"time"
)

// Scribe assembles the system prompt for a figaro agent.
// Implementations are injected into the figaro at creation time.
type Scribe interface {
	// Build returns the assembled system prompt.
	// Called before each prompt to pick up runtime changes.
	// Implementations should cache and only re-template on change.
	Build(ctx Context) (string, error)
}

// Context holds the values exposed to the credo template.
//
// Stage C.6 trimmed this set to identity-only fields that don't
// change per turn. Datetime, cwd, root, model, and tool listings now
// flow through chalkboard.system.* and are surfaced as reminders by
// the provider, not woven into the credo body. Templates referencing
// the removed fields fail loudly at execute time — the surface area
// of the credo is small enough that switching to chalkboard reminders
// is a one-edit migration for users.
type Context struct {
	Provider string // provider name (e.g. "anthropic")
	FigaroID string // figaro instance ID
	Version  string // build version: VCS commit hash
}

// Skill is a skill loaded from a markdown file with frontmatter.
type Skill struct {
	Name        string // from frontmatter or filename
	Description string // from frontmatter
	Content     string // body after frontmatter
	FilePath    string // absolute path to the skill file
}

// DefaultScribe loads credo.md and skills from the config directory.
type DefaultScribe struct {
	credoPath  string // path to credo.md
	skillsDir  string // path to skills directory
	configDir  string // base config directory

	// Cache.
	lastCredo    string
	lastModTime  time.Time
	lastSkillMod time.Time
	cachedPrompt string
	cachedCtx    Context
}

// NewDefaultScribe creates a scribe that reads from the config directory.
//
//	~/.config/figaro/credo.md           — the template
//	~/.config/figaro/skills/            — skill markdown files
func NewDefaultScribe(configDir string) *DefaultScribe {
	return &DefaultScribe{
		credoPath: filepath.Join(configDir, "credo.md"),
		skillsDir: filepath.Join(configDir, "skills"),
		configDir: configDir,
	}
}

func (s *DefaultScribe) Build(ctx Context) (string, error) {
	// Check if credo.md changed.
	credoMod := fileModTime(s.credoPath)
	skillsMod := dirModTime(s.skillsDir)

	if s.cachedPrompt != "" && credoMod.Equal(s.lastModTime) && skillsMod.Equal(s.lastSkillMod) && ctx == s.cachedCtx {
		return s.cachedPrompt, nil
	}

	// Read credo template.
	tmplBytes, err := os.ReadFile(s.credoPath)
	if err != nil {
		return "", fmt.Errorf("read credo.md: %w", err)
	}

	// Parse and execute template.
	tmpl, err := template.New("credo").Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("parse credo template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute credo template: %w", err)
	}

	// Load and append skills.
	skills, err := LoadSkills(s.skillsDir)
	if err != nil {
		// Non-fatal — skills are optional.
		skills = nil
	}

	prompt := buf.String()
	if len(skills) > 0 {
		prompt += "\n\n" + FormatSkills(skills)
	}

	// Cache.
	s.lastModTime = credoMod
	s.lastSkillMod = skillsMod
	s.cachedCtx = ctx
	s.cachedPrompt = prompt

	return prompt, nil
}

// CurrentContext builds a Context from the current runtime state.
// Only identity fields (Provider, FigaroID, Version) — anything that
// changes turn-to-turn lives in the chalkboard.
func CurrentContext(providerName, figaroID string) Context {
	return Context{
		Provider: providerName,
		FigaroID: figaroID,
		Version:  buildVersion(),
	}
}

// version can be set at link time via -ldflags:
//
//	-X github.com/jack-work/figaro/internal/credo.version=abc1234
//
// When empty, buildVersion falls back to the VCS revision embedded by Go.
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

// --- Skills ---

// LoadSkills reads all .md files from a directory, parsing frontmatter.
func LoadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		skill, err := loadSkill(path)
		if err != nil {
			continue // skip malformed skills
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

func loadSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	name, desc, body := parseFrontmatter(string(data))
	if name == "" {
		// Fall back to filename without extension.
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return Skill{
		Name:        name,
		Description: desc,
		Content:     body,
		FilePath:    path,
	}, nil
}

// parseFrontmatter extracts name and description from YAML-style frontmatter.
//
//	---
//	name: foo
//	description: does bar
//	---
//	Body content here.
func parseFrontmatter(content string) (name, description, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", "", content
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return "", "", content
	}

	frontmatter := content[4 : 4+end]
	body = strings.TrimSpace(content[4+end+4:])

	for _, line := range strings.Split(frontmatter, "\n") {
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

// FormatSkills formats skills for appending to the system prompt.
func FormatSkills(skills []Skill) string {
	var b strings.Builder
	b.WriteString("# Available Skills\n\n")
	b.WriteString("Use the read tool to load a skill's file when the task matches its description.\n\n")
	for _, s := range skills {
		b.WriteString(fmt.Sprintf("## %s\n", s.Name))
		if s.Description != "" {
			b.WriteString(fmt.Sprintf("*%s*\n", s.Description))
		}
		b.WriteString(fmt.Sprintf("File: `%s`\n\n", s.FilePath))
	}
	return b.String()
}

// --- Helpers ---

func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func dirModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	// Use the directory's own mod time (changes when files are added/removed).
	return info.ModTime()
}
