package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// Every embedded default template must render bodies that pass the lint.
func TestDefaultTemplates_LintClean(t *testing.T) {
	tmpls, err := form.LoadDefaultTemplates()
	require.NoError(t, err)

	rawString := func(s string) json.RawMessage {
		b, _ := json.Marshal(s)
		return b
	}

	// Old values are populated where the template uses {{.OldString}}.
	prev := form.FromMap(map[string]json.RawMessage{
		"model": rawString("claude-sonnet-4-6"),
	})
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cwd":          rawString("/home/figaro/dev"),
		"datetime":     rawString("Wednesday, April 29, 2026, 10AM EDT"),
		"model":        rawString("claude-opus-4-6"),
		"root":         rawString("/home/figaro/dev"),
		"truncation":   rawString("File foo.go truncated to 2000 lines"),
		"token_budget": rawString("80%"),
	}, nil)

	rendered, err := form.Render(patch, prev, tmpls)
	require.NoError(t, err)
	require.NotEmpty(t, rendered, "default templates must produce some output for the test patch")

	issues := form.Lint(rendered, form.LintOptions{})
	if len(issues) > 0 {
		for _, iss := range issues {
			t.Errorf("default template %q failed lint: %s\n  body: %s", iss.Key, iss.Reason, iss.Body)
		}
	}

	rkeys := make(map[string]bool, len(rendered))
	for _, r := range rendered {
		rkeys[r.Key] = true
	}
	for _, ent := range patch.Entries() {
		if ent.IsRemoval() {
			continue
		}
		k := ent.Key
		assert.True(t, rkeys[k], "expected default template for %q to produce output", k)
	}
}

func TestDefaultTemplates_AllBodiesAreFactual(t *testing.T) {
	tmpls, err := form.LoadDefaultTemplates()
	require.NoError(t, err)

	rawString := func(s string) json.RawMessage {
		b, _ := json.Marshal(s)
		return b
	}

	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cwd":      rawString("/home/figaro"),
		"datetime": rawString("Wednesday, April 29, 2026, 10AM EDT"),
		"model":    rawString("claude-opus-4-6"),
		"root":     rawString("/home/figaro"),
	}, nil)
	rendered, err := form.Render(patch, form.Snapshot{}, tmpls)
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
