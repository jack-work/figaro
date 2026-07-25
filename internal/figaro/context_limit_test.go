package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

// limitProvider is a perfProvider that also reports a context limit.
type limitProvider struct {
	perfProvider
	limit int
}

func (p limitProvider) ContextLimit(model string, snapshot chalkboard.Snapshot) int {
	return p.limit
}

func TestResolveContextLimitPrecedence(t *testing.T) {
	pinned := chalkboard.FromMap(map[string]json.RawMessage{"system.max_context_tokens": json.RawMessage(`200000`)})
	bogus := chalkboard.FromMap(map[string]json.RawMessage{"system.max_context_tokens": json.RawMessage(`"big"`)})

	// The chalkboard pin is an override: it beats the provider both ways.
	assert.Equal(t, 200_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", pinned))
	assert.Equal(t, 200_000,
		resolveContextLimit(limitProvider{limit: 100_000}, "claude-opus-5", pinned))

	// Unset (or unusable): the provider answers.
	assert.Equal(t, 1_000_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", chalkboard.Snapshot{}))
	assert.Equal(t, 1_000_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", bogus))

	// A provider with no opinion still honors the pin, and reports 0 without.
	assert.Equal(t, 200_000, resolveContextLimit(perfProvider{}, "claude-opus-5", pinned))
	assert.Equal(t, 0, resolveContextLimit(perfProvider{}, "claude-opus-5", chalkboard.Snapshot{}))
	assert.Equal(t, 0, resolveContextLimit(limitProvider{limit: 0}, "who-knows", chalkboard.Snapshot{}))
}

var _ provider.Provider = limitProvider{}
var _ provider.ContextLimitProvider = limitProvider{}
