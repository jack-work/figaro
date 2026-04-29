package chalkboard

import (
	"fmt"
	"strings"
)

// LintIssue describes a problem with a rendered reminder body that may
// reduce the model's responsiveness or trip prompt-injection defenses.
// Issues are advisory; callers decide whether to warn or fail.
type LintIssue struct {
	Key    string
	Body   string
	Reason string
}

// LintOptions configures the lint heuristics.
type LintOptions struct {
	// MaxBodyLength caps the length of a single reminder body (after
	// TrimSpace). 0 = no limit. Default if zero: DefaultMaxBodyLength.
	MaxBodyLength int

	// DuplicateText is a list of phrases (typically pulled from the
	// static system prompt) that, if present in a rendered body,
	// indicates redundancy. Empty list = no dup check.
	DuplicateText []string
}

// DefaultMaxBodyLength is the default body cap when MaxBodyLength is 0.
const DefaultMaxBodyLength = 1500

// imperativePrefixes are the phrases that, when starting a body, trip
// Anthropic's prompt-injection defenses or look like override
// instructions. Reminders should sound like factual context, not
// system commands.
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

// Lint inspects the rendered bodies for common phrasing problems.
// Returns one issue per body that fails a check (multiple checks per
// body produce multiple issues).
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
