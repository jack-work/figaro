package anthropicmodels

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/stretchr/testify/assert"
)

func TestContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		// 1M generation.
		{"claude-opus-5", 1_000_000},
		{"claude-opus-5-20260115", 1_000_000},
		{"claude-opus-4-8", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-opus-4-6", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-sonnet-4-6", 1_000_000},
		// Copilot spells the same models with dots; vendor prefixes appear
		// in some configs.
		{"claude-opus-4.6", 1_000_000},
		{"anthropic/claude-opus-5", 1_000_000},
		{"Claude-Opus-5", 1_000_000},

		// 200k generation.
		{"claude-opus-4-5-20251101", 200_000},
		{"claude-opus-4-1-20250805", 200_000},
		{"claude-opus-4-20250514", 200_000},
		{"claude-sonnet-4-5-20250929", 200_000},
		{"claude-haiku-4-5-20251001", 200_000},
		{"claude-haiku-4.5", 200_000},
		{"claude-3-5-sonnet-20241022", 200_000},

		// Unknown: honest zero rather than a guess.
		{"claude-fable-5", 0},
		{"gpt-5.6-terra", 0},
		{"", 0},
		{"claude", 0},
		{"claude-opus-42", 0}, // prefix must land on an id boundary
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ContextWindow(tc.model))
		})
	}
}

func TestCatalogPrefersLearnedWindow(t *testing.T) {
	var c Catalog
	// Unknown model: table says nothing, catalog can teach it.
	assert.Equal(t, 0, c.Window("claude-fable-5"))
	c.Learn("claude-fable-5", 750_000)
	assert.Equal(t, 750_000, c.Window("claude-fable-5"))

	// A learned window beats the table for known models too.
	c.Learn("claude-opus-4-5-20251101", 400_000)
	assert.Equal(t, 400_000, c.Window("claude-opus-4-5-20251101"))
	assert.Equal(t, 1_000_000, c.Window("claude-opus-5"))

	// Garbage from the endpoint is ignored.
	c.Learn("claude-opus-5", 0)
	c.Learn("", 123)
	assert.Equal(t, 1_000_000, c.Window("claude-opus-5"))
}

func TestCatalogContextLimitOverridePrecedence(t *testing.T) {
	var c Catalog

	assert.Equal(t, 1_000_000, c.ContextLimit("claude-opus-5", chalkboard.Snapshot{}))

	// The user's pin wins, whether it is smaller...
	assert.Equal(t, 200_000, c.ContextLimit("claude-opus-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`200000`),
	}))
	// ...or larger than what we would report.
	assert.Equal(t, 2_000_000, c.ContextLimit("claude-opus-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`2000000`),
	}))
	// ...or the model is unknown.
	assert.Equal(t, 500_000, c.ContextLimit("claude-fable-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`500000`),
	}))

	// Junk or non-positive pins are ignored, not treated as a limit of 0.
	assert.Equal(t, 1_000_000, c.ContextLimit("claude-opus-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`"lots"`),
	}))
	assert.Equal(t, 1_000_000, c.ContextLimit("claude-opus-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`0`),
	}))
}

func TestCatalogConcurrentAccess(t *testing.T) {
	var c Catalog
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			c.Learn("claude-opus-5", 1_000_000+i)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		_ = c.Window("claude-opus-5")
	}
	<-done
}
