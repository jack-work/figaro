package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

var _ provider.ContextLimitProvider = (*Provider)(nil)

func TestProviderContextLimit(t *testing.T) {
	p := &Provider{model: "claude-opus-5"}

	assert.Equal(t, 1_000_000, p.ContextLimit("claude-opus-5", chalkboard.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("claude-sonnet-4-6", chalkboard.Snapshot{}))
	assert.Equal(t, 200_000, p.ContextLimit("claude-haiku-4-5-20251001", chalkboard.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("claude-sonnet-4-5-20250929", chalkboard.Snapshot{}))
	assert.Equal(t, 0, p.ContextLimit("some-unreleased-model", chalkboard.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("", chalkboard.Snapshot{}))

	// A window learned from the models endpoint wins over the table...
	p.windows.Learn("some-unreleased-model", 640_000)
	assert.Equal(t, 640_000, p.ContextLimit("some-unreleased-model", chalkboard.Snapshot{}))
	// ...and the user's pin wins over both.
	assert.Equal(t, 42_000, p.ContextLimit("some-unreleased-model", chalkboard.Snapshot{
		"system.max_context_tokens": json.RawMessage(`42000`),
	}))
}
