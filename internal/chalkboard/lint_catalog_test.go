package chalkboard_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// TestDefaultTemplates_LintClean runs every embedded default template
// against representative sample data and asserts that all rendered
// bodies pass the phrasing lint. This is the catalog requirement from
// Stage 5 of plans/SYSTEM-REMINDERS.md: the harness's own templates
// must not start with imperative system-command framing.
func TestDefaultTemplates_LintClean(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	rawString := func(s string) json.RawMessage {
		b, _ := json.Marshal(s)
		return b
	}

	// Build a single patch that exercises every default template key.
	// Old values are populated where the template uses {{.OldString}}
	// (currently only model.tmpl).
	prev := chalkboard.Snapshot{
		"model": rawString("claude-sonnet-4-6"),
	}
	patch := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"cwd":                rawString("/home/figaro/dev"),
			"datetime":           rawString("Wednesday, April 29, 2026, 10AM EDT"),
			"model":              rawString("claude-opus-4-6"),
			"root":               rawString("/home/figaro/dev"),
			"label":              rawString("morning-aria"),
			"truncation":         rawString("File foo.go truncated to 2000 lines"),
			"token_budget":       rawString("80%"),
		},
	}

	rendered, err := chalkboard.Render(patch, prev, tmpls)
	require.NoError(t, err)
	require.NotEmpty(t, rendered, "default templates must produce some output for the test patch")

	issues := chalkboard.Lint(rendered, chalkboard.LintOptions{})
	if len(issues) > 0 {
		for _, iss := range issues {
			t.Errorf("default template %q failed lint: %s\n  body: %s", iss.Key, iss.Reason, iss.Body)
		}
	}

	// Also assert that every template we expected to fire actually
	// produced a rendered entry — guards against accidentally
	// templating to empty strings.
	rkeys := make(map[string]bool, len(rendered))
	for _, r := range rendered {
		rkeys[r.Key] = true
	}
	for k := range patch.Set {
		assert.True(t, rkeys[k], "expected default template for %q to produce output", k)
	}
}

// TestDefaultTemplates_AllBodiesAreFactual checks that every body our
// default templates produce reads as factual context (e.g. "Working
// directory: …", "Model: …") — not as override instructions or
// system commands. Spot-check by lint, plus an explicit assertion that
// no body contains common imperative red flags.
func TestDefaultTemplates_AllBodiesAreFactual(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	rawString := func(s string) json.RawMessage {
		b, _ := json.Marshal(s)
		return b
	}

	patch := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"cwd":      rawString("/home/figaro"),
			"datetime": rawString("Wednesday, April 29, 2026, 10AM EDT"),
			"model":    rawString("claude-opus-4-6"),
			"root":     rawString("/home/figaro"),
			"label":    rawString("aria"),
		},
	}
	rendered, err := chalkboard.Render(patch, chalkboard.Snapshot{}, tmpls)
	require.NoError(t, err)

	imperativeRedFlags := []string{
		"YOU MUST", "ALWAYS", "NEVER", "DO NOT", "OVERRIDE",
		"IGNORE", "DISREGARD", "ATTENTION:", "IMPORTANT:",
	}
	for _, r := range rendered {
		for _, flag := range imperativeRedFlags {
			assert.NotContains(t, r.Body, flag,
				"default template %q should not contain imperative phrasing %q (body: %q)",
				r.Key, flag, r.Body)
		}
	}
}
