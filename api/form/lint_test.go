package form

import (
	"fmt"
	"strings"
)

// LintIssue is advisory.
type LintIssue struct {
	Key    string
	Body   string
	Reason string
}

type LintOptions struct {
	// MaxBodyLength zero means DefaultMaxBodyLength.
	MaxBodyLength int
	// DuplicateText phrases found in a body indicate redundancy.
	DuplicateText []string
}

const DefaultMaxBodyLength = 1500

// imperativePrefixes trip prompt-injection defenses; reminders should read
// as factual context.
var imperativePrefixes = []string{
	"YOU MUST",
	"YOU SHOULD",
	"NEVER ",
	"ALWAYS ",
	"DO NOT ",
	"IGNORE ",
	"DISREGARD ",
	"OVERRIDE ",
	"IMPORTANT:",
	"ATTENTION:",
}

// Lint returns one issue per failed check per body.
func Lint(rendered []RenderedEntry, opts LintOptions) []LintIssue {
	max := opts.MaxBodyLength
	if max == 0 {
		max = DefaultMaxBodyLength
	}

	var issues []LintIssue
	for _, r := range rendered {
		body := strings.TrimSpace(r.Body)
		upper := strings.ToUpper(body)
		for _, p := range imperativePrefixes {
			if strings.HasPrefix(upper, p) {
				issues = append(issues, LintIssue{
					Key:    r.Key,
					Body:   body,
					Reason: fmt.Sprintf("imperative phrasing: starts with %q (use factual phrasing instead)", p),
				})
				break
			}
		}
		if len(body) > max {
			issues = append(issues, LintIssue{
				Key:    r.Key,
				Body:   body,
				Reason: fmt.Sprintf("body exceeds %d characters (%d)", max, len(body)),
			})
		}
		for _, dup := range opts.DuplicateText {
			if dup == "" || len(dup) < 16 {
				continue // too short to meaningfully detect duplication
			}
			if strings.Contains(body, dup) {
				issues = append(issues, LintIssue{
					Key:    r.Key,
					Body:   body,
					Reason: "duplicates static system-prompt content",
				})
				break
			}
		}
	}
	return issues
}
