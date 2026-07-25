package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

var _ provider.ContextLimitProvider = (*Anthropic)(nil)

func TestAnthropicContextLimit(t *testing.T) {
	a := &Anthropic{Model: "claude-opus-5"}

	// From the verified table.
	assert.Equal(t, 1_000_000, a.ContextLimit("claude-opus-5", chalkboard.Snapshot{}))
	assert.Equal(t, 200_000, a.ContextLimit("claude-sonnet-4-5-20250929", chalkboard.Snapshot{}))
	assert.Equal(t, 0, a.ContextLimit("some-unreleased-model", chalkboard.Snapshot{}))

	// Empty model falls back to the configured one.
	assert.Equal(t, 1_000_000, a.ContextLimit("", chalkboard.Snapshot{}))

	// The chalkboard pin overrides.
	assert.Equal(t, 250_000, a.ContextLimit("claude-opus-5", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`250000`),
	}))
}
