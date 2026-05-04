package credo

import (
	"fmt"
	"strings"
)

// FormatSkills was an alternate skill-catalog renderer; today only
// tests still call it. Lives here so it doesn't ship in the binary.
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
