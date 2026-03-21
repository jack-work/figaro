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

// Context holds the runtime values available to the credo template.
type Context struct {
	DateTime  string // current date/time, hour precision
	Cwd       string // working directory of the calling process
	Root      string // project root (git root or explicit)
	Provider  string // provider name (e.g. "anthropic")
	Model     string // model ID (e.g. "claude-sonnet-4-20250514")
	FigaroID  string // figaro instance ID
	Tools     string // formatted tool list (statically constructed)
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
func CurrentContext(cwd, root, providerName, model, figaroID, tools string) Context {
	now := time.Now()
	return Context{
		DateTime: now.Format("Monday, January 2, 2006, 3PM MST"),
		Cwd:      cwd,
		Root:     root,
		Provider: providerName,
		Model:    model,
		FigaroID: figaroID,
		Tools:    tools,
	}
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
